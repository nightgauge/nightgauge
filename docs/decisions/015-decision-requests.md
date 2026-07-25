# DecisionRequests — Schema, Local-First Transport, and the Surface Contract

**Date:** 2026-07-19
**Author:** nightgauge
**Status:** Decided
**Issue:** #323 (Epic #322: Action Center)
**Builds on:** ADR 013 "Run Lifecycle Trace Schema" (#179) and ADR 014 "Live
Trace Transport" (#234) — both indexed in [README.md](README.md);
adaptive stall recovery (#3005,
[004-adaptive-stall-recovery.md](004-adaptive-stall-recovery.md)),
the JSONL append primitive (`internal/history.AppendJSONL`), the remote
command channel (`internal/platform/commands.go`, #3557/#3551), and the
one-way Discord notifier (`internal/notify`).

> **Amendment in flight.** This ADR describes the run-scoped Action Center as
> shipped in E1. Repo-scoped attention — the `attention sweep` evaluation loop,
> standing-condition semantics, and `auto_resolved` as a terminal state
> distinct from a human resolution — extends this contract and is tracked in
> epic #88. Read this document as the base contract, not the current whole.

---

## Executive Summary

Every consequential dead-end in the fleet today is **one-way**. When an
`owner-action` issue burned $2.71 and failed, when the autonomous scheduler
stopped from work exhaustion, and when a stage went quiet for 19 minutes, the
operator's only signal was a bare Discord webhook embed — no reason, no
options, no way to answer from the surface that alerted them. There is no
primitive for _"the pipeline needs a human decision,"_ no inbox of pending
decisions, and no channel back into the run.

This ADR fixes the contract for a first-class **`DecisionRequest`**: a durable,
local-first record that any component raises when human input is needed, that
every surface can list, subscribe to, and resolve, and whose resolution routes
**only to deterministic verbs the system already trusts**. The design decisions:

1. **A schema** (Decision A) with a closed `kind` set, ADR-013 trace linkage,
   machine-actionable `options[]` bound to a **verb registry** (Decision B),
   free-text steer that becomes pinned next-stage context (Decision G), and a
   `open → acknowledged → resolved | expired` lifecycle with full audit.
2. **Local-first transport** (Decision C): `.nightgauge/attention/` is the
   source of truth, with a **single authoritative writer** (the Go binary) —
   the hard lesson from the #316 write races. Platform sync is additive.
3. **A Surface contract** (Decision E): `list` / `subscribe` / `resolve`, such
   that the VSCode extension (IPC), the dashboard (platform API + SSE push),
   and a **future two-way Discord bot** (interactions endpoint) bind with zero
   pipeline refactor. Mattermost fits the same contract and is deprioritized.
4. **Resolution semantics** (Decision D): one queue, many mirrors — resolve on
   any surface and the card disappears everywhere; idempotent, deduped,
   expiring with declared defaults.
5. **A producer contract and the initial eight producers** (Decision F), each
   pinned to a real trigger point in the existing code.
6. **A dashboard alerting model and card anatomy** (Decision I) with mockups —
   beauty is a stated requirement.

Non-goals for E1 (Decision K): Discord/Mattermost implementations, Flutter,
and monitoring-agent auto-resolution are **designed-for, not built**.

## Context

- **The trace already knows _why_ a run is stuck.** ADR 013 gave every run an
  append-only decision trace at `.nightgauge/pipeline/trace/<run_id>.jsonl`
  keyed by `(run_id, producer, seq)`. A DecisionRequest is raised _from_ a
  trace node and links back to it — the trace explains the decision; the
  DecisionRequest asks a human to make one.
- **A dashboard→pipeline command channel already exists.** The platform pushes
  `PendingCommand`s that the local agent polls
  (`internal/platform/commands.go` — `CommandService`, `PendingCommand`,
  `AcknowledgeCommand`), executes, and acknowledges via the
  `agent.acknowledgeCommand` IPC method (`internal/ipc/agent_commands.go`,
  #3551); the platform-assigned `run_id` threads through as `RemoteRunID`
  (#3557). This is the "dashboard-trigger→remoteRunId ack path" the epic says
  to generalize — resolving a DecisionRequest from the dashboard is exactly a
  `PendingCommand`.
- **The verbs already exist (mostly).** The Go binary exposes deterministic
  operations over CLI (`cmd/nightgauge/main.go`) and IPC
  (`internal/ipc/server.go`, `s.methods["namespace.method"]`; the authoritative
  inventory is `internal/ipc/ipc_contract_test.go`). The Action Center adds no
  new _mutation surface_ — it fronts commands we already trust, plus two small,
  well-scoped verbs the fleet lacks (Decision B).
- **A feedback-context injection path already exists.** Adaptive stall recovery
  (ADR 004) writes `feedback-{N}.json` under `.nightgauge/pipeline/` and the
  scheduler consumes it (`FeedbackFile` →
  `RetryEngine.EvaluateBacktrack`) to steer the run's next stage. Operator
  steer text rides this same path (Decision G) — no new context channel.
- **The append primitive is settled.** `internal/history.AppendJSONL`
  serializes appends with an in-process mutex over POSIX `O_APPEND`, keeping
  every JSONL store byte-equivalent. The attention journal reuses it.
- **The notices are one-way.** The stuck-epic watchdog POSTs Discord embeds
  with no reply channel (`internal/orchestrator/autonomous_stuck_epic.go` —
  `alertStuckEpics` → `internal/notify.PostEmbeds`), and cascade pause fires
  only a one-way `autonomous.statusChanged` IPC status event
  (`internal/ipc/server.go` `OnStatusChange`). Neither can receive an answer.
  The DecisionRequest is the two-way replacement.

## Decision

### A — The `DecisionRequest` schema

One JSON object per request. The envelope mirrors ADR-013 conventions
(`schema_version`, additive payloads, `(producer, idempotency)` identity) so
readers tolerate unknown fields and new kinds without a version bump.

| Field             | Type   | Req | Semantics                                                                          |
| ----------------- | ------ | --- | ---------------------------------------------------------------------------------- |
| `schema_version`  | int    | yes | Envelope version, currently `1`. Payloads evolve additively.                       |
| `id`              | string | yes | `dr_<uuidv7>` — stable identity and the resolution idempotency key.                |
| `idempotency_key` | string | yes | `<producer>:<scope>` — at most **one open** request per key (Decision D).          |
| `kind`            | enum   | yes | `unblock \| approve \| choose \| provide_input \| handoff \| resume` (closed set). |
| `severity`        | enum   | yes | `fyi \| blocking_run \| blocking_fleet` — drives alerting and SLA (Decision I).    |
| `title`           | string | yes | Human-facing _what_ (e.g. "Fleet idle — 4 Backlog items promotable").              |
| `body`            | string | yes | Human-facing _why_: the precise blocker, and any checklist (handoff).              |
| `context`         | object | yes | See below — repo/issue/run/stage/cost + the ADR-013 trace ref.                     |
| `producer`        | string | yes | Registered producer name (Decision F).                                             |
| `options`         | array  | yes | Machine-actionable choices, each bound to a registry verb (Decision B).            |
| `steer`           | object | no  | `{ enabled, hint }` — free-text steering (Decision G). Absent ⇒ no steer box.      |
| `created_at`      | string | yes | RFC3339Nano UTC.                                                                   |
| `expires_at`      | string | yes | RFC3339Nano UTC; the sweep resolves-as-expired past this (Decision D).             |
| `default_action`  | string | yes | Option `id` applied on expiry, or `expire_noop` (a declared, safe default).        |
| `lifecycle`       | object | yes | State machine + audit (below).                                                     |

**`context`** carries everything a card needs without a join, plus the trace
back-reference:

```jsonc
{
  "repo": "owner/name",
  "issue": 323,
  "run_id": "<run uuid>", // absent for fleet-scoped requests (work exhaustion)
  "stage": "pr-merge", // absent for run-scoped/fleet-scoped requests
  "cost_so_far_usd": 2.71, // the operator's own run spend, for context
  "blocker": "required check `CI` is failing; branch protection blocks merge",
  "trace_ref": { "run_id": "<run uuid>", "producer": "go", "seq": 118 },
}
```

`trace_ref` points at the exact ADR-013 trace node that raised the request
(e.g. the `gate_result` or `outcome` event), so the card can deep-link into
the Lifecycle Explorer and the audit is bidirectional.

**`options[]`** — each option is a button, never prose:

```jsonc
{
  "id": "promote",
  "label": "Promote to Ready",
  "verb": "project.syncStatus", // MUST be in the verb registry (Decision B)
  "args": { "status": "ready", "then": "autonomous.rescan" },
  "style": "primary", // primary | default | danger — visual weight only
}
```

**`lifecycle`** — the state machine and its audit fields:

```jsonc
{
  "state": "open", // open → acknowledged → resolved | expired
  "acknowledged": { "actor": "octocat", "at": "…" }, // optional
  "resolved": {
    "actor": "octocat",
    "at": "…",
    "option_id": "promote",
    "steer_text": null, // present only when the operator typed steering
    "note": "promoting the two docs-only items",
  },
  "expired": { "at": "…", "applied": "default_action" }, // mutually exclusive with resolved
}
```

`acknowledged` is optional and non-blocking — a surface may mark a request seen
(clearing its badge) without resolving it. Terminal states are `resolved` and
`expired`; both are audited into the trace and the run history record
(Decision H).

### B — Options are commands: the verb registry

Every `option.verb` MUST resolve to an entry in a **closed verb registry** — a
Go allowlist of deterministic operations. This is the security boundary
(Decision J): a resolution can trigger _only_ a registered verb, with args
bounded by the request. The Action Center invents no mutation it does not
already trust.

**Verbs that exist today** (wired directly):

| Registry verb                   | Backing primitive                                                        | Surface(s)         |
| ------------------------------- | ------------------------------------------------------------------------ | ------------------ |
| `queue.add`                     | `queue add` / IPC `queue.add` / `Scheduler.QueueAdd`                     | CLI, IPC, internal |
| `issue.removeBlockedBy`         | `issue remove-blocked-by` / `IssueService.RemoveBlockedByNumber`         | CLI, internal¹     |
| `autonomous.resume`             | IPC `autonomous.resume` / `AutonomousScheduler.Resume()`                 | IPC, internal      |
| `autonomous.rescan`             | IPC `autonomous.rescan`                                                  | IPC                |
| `autonomous.complete`           | IPC `autonomous.complete` → `NotifyComplete` → `promoteUnblockedToReady` | IPC, internal      |
| `autonomous.clearIssueFailures` | IPC `autonomous.clearIssueFailures`                                      | IPC                |
| `project.syncStatus`            | `project sync-status` / IPC `project.syncStatus` / `board.updateStatus`  | CLI, IPC, internal |
| `issue.close`                   | IPC `issue.close`                                                        | IPC                |

¹ `issue.removeBlockedBy` exists as a CLI verb and internal method but has **no
IPC method today**; E1 adds the thin IPC wrapper (it is a pure re-export of the
existing internal call, not new mutation logic).

**Verbs E1 must add** — the fleet lacks a trusted operator-invokable form, so
each is a small new verb fronting an _existing enforcement path_ (never new
business logic):

| Registry verb             | Why it does not exist yet                                                                                                                  | E1 insertion point                                                                                                                                               |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `budget.raiseCeiling`     | The ceiling is a config value (`config.BudgetCeiling`) set once via `--budget`; enforcement is read-only and terminal.                     | A **runtime ceiling override** in run state, honored by `internal/orchestrator/safety_rails.go` before the `budget_ceiling_hit` terminal (`failure_handler.go`). |
| `run.retryWithEscalation` | Model escalation is automatic inside failure handling (`internal/intelligence/failure/taxonomy.go`, `Escalate`), never operator-invokable. | A re-dispatch that clears the failure cooldown (`autonomous.clearIssueFailures`) **and** forces the next model tier for the retry.                               |

This honesty is a design decision, not a gap: the epic's "adds no new mutation
surface" means _no mutation the operator could not already trigger through a
trusted deterministic path_. `budget.raiseCeiling` and `run.retryWithEscalation`
are themselves deterministic, audited, and registry-gated. A DecisionRequest
whose producer wants budget-raise or escalate-retry declares those verbs; a
resolution is rejected (Decision J) if the verb is not registered — so the two
verbs land in E1 alongside the producers that need them (Decision F: budget
ceiling, watchdog).

### C — Local-first transport and the single-writer rule

`.nightgauge/attention/` under the workspace root is the **source of truth**.
The store works with zero hosted platform; the extension is a complete surface
on top of it. Layout:

```
.nightgauge/attention/
  <id>.json          # materialized current state of one request (the read model)
  journal.jsonl      # append-only lifecycle audit (created/ack/resolved/expired)
```

- **Materialized files** are the read model — a surface lists by scanning
  `*.json`. Because a request is _mutable_ (unlike an append-only trace), the
  current state lives in one file, replaced atomically by **write-temp +
  rename** (the same pattern `internal/orchestrator/stall_recovery.go` uses for
  `feedback-{N}.json`), so a reader never observes a half-written record.
- **The journal** is append-only via `internal/history.AppendJSONL` — every
  lifecycle transition is one line, byte-equivalent with every other JSONL
  store, giving a durable audit even if a materialized file is later pruned.

**Single authoritative writer (the #316 lesson).** Exactly one process — the Go
binary hosting the scheduler / IPC server — writes `.nightgauge/attention/`.
Surfaces **never** write these files directly. A surface's only mutation is
`resolve` (Decision E), which routes to the single writer (locally via IPC, or
from the platform via a `PendingCommand`); the writer applies the lifecycle
transition and persists. Concurrent goroutines inside that process serialize
through the same in-process mutex the append path already provides. This
eliminates by construction the multi-writer races #316 hit — there is no
"resolve on disk from two places" path to lose.

**Identity and idempotency.** `id` (`dr_<uuidv7>`) is the durable key.
`idempotency_key` enforces **at most one open request per condition**: a
producer that re-detects the same blocker (e.g. the fleet is still exhausted on
the next scan) **updates** the open request in place — it does not spawn a
duplicate. Resolution is a compare-and-set on `lifecycle.state`
(`open|acknowledged → resolved`); applying the same resolve twice is a no-op.

**Expiry sweep.** Each request declares `expires_at` and a `default_action`. A
sweep — piggybacking the scheduler's existing periodic scan (the same loop that
runs stuck-epic detection and the survival sweep) — transitions expired `open`
requests to `expired`, applying `default_action` (which may be `expire_noop`).
The sweep is idempotent and is itself the single writer, so expiry cannot race
a concurrent resolve (the CAS loses gracefully). No request lingers forever;
none silently mutates the fleet without a declared default.

### D — Resolution semantics: one queue, many mirrors

The single writer + `.nightgauge/attention/` **is** the queue. Every surface
renders a **mirror** and every resolve converges on the one writer:

- **Resolve anywhere, disappears everywhere.** A resolve carries
  `(id, option_id, actor, steer_text?)`. The writer validates and applies it
  once (CAS), executes the option's verb, and the state change propagates to
  every mirror — the IPC push updates the VSCode tree, the SSE push
  (Decision E) updates the dashboard, and a future Discord message edits to
  "resolved by @octocat." The card vanishes from all surfaces.
- **Idempotent + deduped.** Request creation dedupes on `idempotency_key`;
  resolution dedupes on `id` + terminal-state CAS. A replayed resolve (e.g. a
  `PendingCommand` redelivered after a reconnect) is a safe no-op, exactly as
  ADR-013/014 make trace application idempotent on `(run_id, producer, seq)`.
- **Expiring with declared defaults.** Unresolved requests expire per Decision
  C. Expiry is a first-class terminal state, audited like a resolution.

### E — The Surface contract

Every surface implements one interface; new surfaces plug in with **zero
pipeline refactor** because the pipeline knows only the contract, never a
surface:

```
Surface {
  list(filter)     -> DecisionRequest[]     // open (and optionally recent) requests
  subscribe(onEvent) -> Disposable          // push: created | updated | resolved | expired
  resolve(id, option_id, actor, steer_text?) -> Result  // the ONLY mutation
}
```

`resolve` is the sole write path and always terminates at the single Go writer
(Decision C). Binding per surface:

- **VSCode extension (IPC).** New `//ipc:method` registrations on
  `internal/ipc/server.go` (structs in `internal/ipc/protocol.go`):
  `attention.list`, `attention.resolve`, and `attention.acknowledge`; live
  updates ride the existing Go→TS event push (the newline-delimited
  `on("attention.event")` channel `IpcClientBase` already dispatches, the same
  channel that streams pipeline progress). They enter the typed client via the
  mandatory `make generate-ipc-client` step →
  `packages/nightgauge-vscode/src/services/IpcClient.generated.ts`. The UI is a
  new `nightgauge.attentionView` (contributed in `package.json`
  `contributes.views`, constructed with `createTreeView` in
  `packages/nightgauge-vscode/src/bootstrap/services.ts`) backed by a new
  `AttentionTreeProvider.ts` alongside the existing providers in
  `packages/nightgauge-vscode/src/views/` — the live-updating
  `WorkflowTreeProvider` is the closest model. A count badge via the
  `TreeView.badge` API, toasts via `NotificationService`, and actions via
  `showQuickPick` (options) + `showInputBox` (steer).
- **Dashboard (platform API + SSE push).** Contract-level endpoints the hosted
  platform implements:

  | Endpoint                         | Purpose                                                        |
  | -------------------------------- | -------------------------------------------------------------- |
  | `GET /v1/attention`              | List open (and recently resolved) requests for the workspace.  |
  | `GET /v1/attention/:id`          | Fetch one request (card detail + history).                     |
  | `POST /v1/attention/:id/resolve` | Resolve: `{ option_id, steer_text? }`. Validated (Decision J). |

  Live push **reuses the ADR-014 SSE pattern**, but on an **account/workspace**
  stream rather than a run stream (a DecisionRequest can outlive or precede any
  single run — e.g. work exhaustion). A new event type `attention.event`
  carries `{ action, request }`. Resolution does **not** mutate the local
  pipeline directly: it rides the existing agent-command envelope. The platform
  publishes a resolve `PendingCommand` on the agent command stream
  (`GET /v1/agents/{agentId}/commands`, consumed by
  `AgentCommandStreamService` via a new `CommandHandler`, with the poll
  fallback `internal/platform/commands.go` `PollCommands`); the single writer
  executes it and acknowledges via `agent.acknowledgeCommand`
  (`POST /v1/agents/{agentId}/commands/{id}/ack`). This is the same
  dashboard-trigger→`remoteRunId` ack path (#3557/#3551) the `TriggerCommandHandler`
  already proves, generalized from one command to the whole verb registry.
  The platform authenticates with the existing `pipelineAuth` (a JWT
  `NIGHTGAUGE_API_KEY` or the license key), scoped per account/workspace via
  the `agentId` (Decision J).

- **Future two-way Discord bot (interactions endpoint) — E2, designed here.**
  Discord button presses arrive at an interactions webhook; the bot maps a
  button `custom_id` → `(request_id, option_id)` and calls the **same**
  `POST /v1/attention/:id/resolve`. The one-way `internal/notify` `PostEmbeds`
  becomes the _notify_ half; the interactions endpoint is the _receive_ half.
  **Zero pipeline refactor**: the bot is just another `resolve` client. This
  drives the schema — options carry a short `label` and stable `id` precisely
  so a Discord button can render and round-trip them.
- **Mattermost (deprioritized).** The slash/outgoing bridge
  ([../MATTERMOST_INTEGRATION.md](../MATTERMOST_INTEGRATION.md)) satisfies the
  same `resolve` contract; it is not designed around here.

### F — The producer contract and the initial eight producers

A **producer** is any component that raises a DecisionRequest. The contract:
_at the trigger point, instead of (or in addition to) a one-way notify, call
`attention.raise(request)` with a stable `idempotency_key`, a declared
`default_action`/`expires_at`, and options bound to registry verbs._ Producers
never write `.nightgauge/attention/` directly — `attention.raise` routes to the
single writer.

| #   | Producer                   | Trigger point (file · condition)                                                                                                                               | kind            | Options → verbs                                                                                | Default / expiry                    |
| --- | -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------- | ---------------------------------------------------------------------------------------------- | ----------------------------------- |
| 1   | Work exhaustion            | `autonomous.go` `runCycle` idle branch (`remaining==0 && runningCount==0`) + `prioritize` `bump(reason)` skip-tally · nothing dispatchable                     | `choose`        | promote (`project.syncStatus`→ready + `autonomous.rescan`) · leave                             | leave · 24h                         |
| 2   | Owner-action handoff       | `autonomous.go` `prioritize` `excludedLabelMatch` vs `DefaultExcludeLabels="owner-action"` (`config.go`, #317) · silent human-only skip                        | `handoff`       | mark-done & requeue-dependents (`issue.close` + `autonomous.complete`) · snooze                | none (needs a human) · 7d           |
| 3   | Cascade pause              | `autonomous.go` `onPipelineComplete` → `cascade_tracker.go` `RecordFailure` → `Status="safety_tripped"` · N failures in window                                 | `resume`        | resume (`autonomous.resume`) · keep-paused · open-triage                                       | keep-paused · none                  |
| 4   | Budget ceiling hit         | `budget_enforcer.go` `CheckPipelineBudget` → scheduler gate → `failure_handler.go` `TerminalKindBudgetExceeded` · over ceiling                                 | `approve`       | raise-to-$X (`budget.raiseCeiling`) · halt (terminal)                                          | halt · 1h                           |
| 5   | blockedBy deferral         | `autonomous.go` `prioritize` `bump("blocked-by-dep")` / `onPipelineComplete` `TerminalKindBlockedDependency` (#305/#310)                                       | `choose`        | remove-stale-edge (`issue.removeBlockedBy`) · reorder (`queue.add`) · leave                    | leave · 72h                         |
| 6   | Branch-protection block    | `stages/prmerge.go` `Decide`→`PathPunt`; taxonomy `CatRulesetBlocked` (`taxonomy.go`, `Escalate:true`); [PR merge](../PR_MERGE_STAGE.md)                       | `unblock`       | open-PR (link, human fixes) · retry-after-fix (`autonomous.clearIssueFailures` + `rescan`)     | none (needs a human) · 48h          |
| 7   | Definitive auth failure    | `identity_preflight.go` `CheckIdentity` (fail-closed) / taxonomy `CatPermission` (401/403); [adapter doctor](../ADAPTER_DOCTOR.md)                             | `provide_input` | login-and-retry (guidance + `autonomous.clearIssueFailures`) · halt                            | halt · 12h                          |
| 8   | Watchdog / health findings | `autonomous_stuck_epic.go` `surfaceStuckEpics`; TS stall detector → `stall_recovery.go` `ClassifyStallSignal`; [health](../HEALTH_MONITORING.md) · stage quiet | `choose`        | wait · kill+retry (`pipeline.stop` + `queue.add`) · escalate-model (`run.retryWithEscalation`) | wait (bounded), then escalate · 30m |

Each producer replaces a dead-end that is today silent or one-way. Producers
2 and 8 are the direct fixes for the motivating incidents (the invisible
owner-action skip; the 19-minute quiet stage). Producers 4 and 8 are the ones
that require the two E1 verbs from Decision B.

### G — Free-text steer becomes pinned next-stage context

Steer is the **only** open-ended input, and it becomes _context, never a
command_ (Decision J). It rides the existing feedback-context path (ADR 004),
so the Action Center adds no new context channel:

1. On resolve with `steer_text`, the single writer appends it to the target
   issue's `.nightgauge/pipeline/feedback-{N}.json` (read-merge-write, the same
   carrier stall recovery and `writePrMergeRetryFeedback` use), as a new
   `OPERATOR_STEER` signal: `severity: "warning"`, `backtrack_target_stage:
null`, the operator text in `rationale`, and an operator-origin marker (the
   analogue of `stallRecoveryRationalePrefix`) so audits distinguish operator
   steering from agent- and scheduler-synthesized signals.
2. Because it is `warning`, not `blocking`, `RetryEngine.EvaluateBacktrack`
   (`internal/orchestrator/retry_engine.go`, which acts only on `blocking`
   signals with a non-empty target) **ignores it** — the steer is pure context
   and never triggers a rewind. The next stage's skill feedback-intake reads it
   verbatim (`skills/nightgauge-feature-planning/_includes/feedback-and-context.md`,
   `skills/nightgauge-feature-dev/_includes/context-and-feedback-intake.md`),
   pinning it as background the stage must honor. This mirrors the existing
   `PR_MERGE_RETRY` precedent, which already carries free text into a stage
   retry as warning-severity context.
3. Schema: add `OPERATOR_STEER` to the closed `signal_type` enum in
   `packages/nightgauge-sdk/src/context/schemas/feedback.ts` and the mirrored
   Go `FeedbackSignal` in `internal/orchestrator/retry_engine.go`. The file is
   transient (cleaned up at `/pr-merge`), which is correct for a per-run steer.

See [../CONTEXT_ARCHITECTURE.md](../CONTEXT_ARCHITECTURE.md) and
[../FEEDBACK_LOOPS.md](../FEEDBACK_LOOPS.md) for the stage-to-stage handoff this
reuses.

### H — Audit integration (trace + run history)

Every terminal transition is recorded in **three** places, none duplicating the
others:

- **The ADR-013 decision trace** gains one additive kind,
  `decision_request` (payload: `id`, `kind`, `producer`, resolution
  `option_id`/`actor`/`note` or expiry, and the originating `trace_ref.seq`).
  Emitted to `.nightgauge/pipeline/trace/<run_id>.jsonl` for run-scoped
  requests, it closes the loop: the node that _raised_ the request and the node
  that _records its resolution_ are joined by `id`. New kind ⇒ no
  `schema_version` bump (ADR-013 forward-compat rule).
- **The run history record** (`internal/state`, the V3 RunRecord joined by
  `run_id`) carries the resolution so a completed run answers "who unblocked
  this, when, why" without reading the attention store.
- **The attention journal** (`journal.jsonl`, Decision C) is the store's own
  durable audit, independent of run retention.

Fleet-scoped requests (no `run_id`, e.g. work exhaustion) skip the run-trace
leg and are audited via the journal alone. Payload hygiene follows ADR-013: no
secrets, tokens, or raw logs — decision context only.

### I — Dashboard alerting model, card anatomy, and mockups

**Alerting a logged-in user.** The dashboard subscribes to the workspace
`attention.event` SSE stream (Decision E). On a new request:

- **`blocking_fleet`** → immediate toast + inbox badge increment + optional
  sound; the fleet is stopped, so it is interrupt-worthy.
- **`blocking_run`** → inbox badge + a subtle in-app toast; one run waits.
- **`fyi`** → badge only; no interruption.

The badge shows the unread count; `acknowledge` (Decision A) clears it without
resolving. **Web push** (browser notifications when the tab is closed) is a
later phase, not E1 — the live in-app path lands first. Time-to-decision (the
gap between `created_at` and the terminal state) is recorded per request for a
"decision latency" metric.

**Card anatomy** — what / why / cost / context / options / steer / history:

```
┌─ ⛔ Fleet stopped — cascade circuit breaker tripped ──────── 4m ago ─┐
│ WHY   6 pipeline failures in 10 min across octocat/acme-web,        │
│       octocat/acme-api → safety:circuit-breaker. Manual triage.     │
│ WHERE octocat/acme-web · run 0c1f… · cost so far $4.10  [trace ↗]   │
│                                                                     │
│  [ Resume fleet ]   [ Keep paused ]   [ Open triage ↗ ]             │
│                                                                     │
│  ▸ Steer (optional): ______________________________________        │
│    "skip acme-web this wave, it's a flaky test"                     │
│                                                                     │
│  history: raised by cascade-breaker · default: keep-paused in 0m    │
└─────────────────────────────────────────────────────────────────────┘
```

**Inbox layout** — a day-view worth living in; severity rail on the left,
most-severe-then-newest first, resolved items collapse to a thin audit strip:

```
  ATTENTION  (3)                                         ◍ live   ⟳ 2s ago
  ────────────────────────────────────────────────────────────────────────
  ┃⛔  Fleet stopped — cascade breaker tripped          octocat/acme-web  4m
  ┃⚠  Budget ceiling hit — $12.00 of $12.00            octocat/acme-api  9m
  ┃●  owner-action: Cloudflare DNS checklist           octocat/acme-web  1h
  ────────────────────────────────────────────────────────────────────────
  resolved today (7)  ·  median time-to-decision 3m12s          [ show ▾ ]
```

Empty state (the goal, most of the day):

```
                          ✓  All clear
             No decisions pending. The fleet is steering itself.
                    Last decision resolved 41m ago.
```

**VSCode tree section** — the same queue, mirrored into the sidebar; a badge on
the view container, quick-pick on click:

```
  NIGHTGAUGE: ATTENTION                                    3 ⬤
  ▾ Blocking (2)
      ⛔ Fleet stopped — cascade breaker tripped        acme-web
      ⚠  Budget ceiling hit — $12.00                    acme-api
  ▾ Needs a human (1)
      ●  owner-action: Cloudflare DNS checklist         acme-web
  ─────────────────────────────────────────────────────────────
  ✓ all clear when empty
```

Beauty is a requirement, not decoration: the severity rail gives instant
triage, the empty state is the reward, and the same three primitives (badge,
card, steer box) render identically across dashboard and extension because they
read the same queue.

### J — Security

Following [../../standards/security.md](../../standards/security.md):

- **Options are commands — validate the option, not the prose.** A resolve
  names an `option_id`. The platform validates it against the request's declared
  `options[]` **server-side** and the surface validates **client-side**; the
  single Go writer **re-validates** against the persisted request _and_ against
  the verb registry before executing (defense in depth). A resolve naming an
  unknown option, or an option whose verb is not registered, is **rejected** —
  a surface can never conjure a verb or arg the producer did not declare.
- **Steer text is context, never shell.** It is written to a JSON
  feedback-context record and injected as prompt context (Decision G); it never
  reaches a shell, is never interpolated into a command string, and passes
  through the existing prompt-injection sanitization layer as untrusted input.
- **Auth scoping per account/workspace.** The platform API authenticates with
  the existing `pipelineAuth` license auth; a user may list/resolve only
  requests for workspaces they are authorized on, checked on **every** request
  (not just at list time). Authorization failures are logged; the actor is
  recorded on every resolution for audit.
- **No secrets in requests.** `context`, `body`, and payloads carry decision
  context only — no tokens, credentials, or raw logs (ADR-013 payload hygiene).
- **Error hygiene.** Resolution failures return a generic client error; details
  are logged internally, never surfaced to the card.

### K — Non-goals for E1 (designed-for, not built)

- **Discord / Mattermost implementations.** The interactions endpoint (E2) and
  the Mattermost bridge are designed to fit the Surface contract (Decision E)
  but are not built in E1.
- **Flutter push + action sheet (E3).** The same platform API serves it later.
- **Monitoring-agent auto-resolution (E4).** The health/monitoring layer filing
  DecisionRequests and, where confident, auto-resolving them is a producer
  role designed-for here (Decision F, producer 8) but not automated in E1.

E1 delivers exactly two surfaces — the VSCode Action Center tree and the
dashboard Attention inbox — over one local-first queue.

## Alternatives considered

- **Reuse the ADR-013 trace as the store** (raise/resolve as trace events).
  Rejected: the trace is append-only and per-run; DecisionRequests are
  **mutable** (lifecycle transitions) and frequently **fleet-scoped** (no
  `run_id`). The trace is the wrong shape for a mutable, cross-run inbox — but
  it is the right place to _audit_ resolutions (Decision H).
- **Let each surface write `.nightgauge/attention/` directly.** Rejected — this
  is precisely the multi-writer race #316 hit. A single authoritative writer
  with `resolve`-only surfaces (Decision C) removes the race by construction.
- **A new platform WebSocket for attention.** Rejected for the same reason ADR
  014 rejected it for trace: the SSE stream already provides auth, keepalive,
  reconnection replay, and fan-out. Attention rides an account/workspace SSE
  channel, not a new socket.
- **Let options carry arbitrary commands / free-form action strings.**
  Rejected — this is an RCE surface. Options bind only to a closed verb registry
  (Decision B); the registry _is_ the security boundary (Decision J).
- **Platform-required (no local store).** Rejected — the epic's local-first
  principle: the extension must be a complete surface with zero hosted platform.
  Platform sync is additive.
- **Add all eight verbs as new commands.** Rejected — six of eight map to
  existing trusted verbs; inventing new mutation logic for them would duplicate
  and diverge from the paths the CLI/IPC already expose. Only the two genuinely
  missing verbs are added, each fronting an existing enforcement path.

## Consequences

- Any component that today hits a silent or one-way dead-end (Decision F) SHOULD
  raise a DecisionRequest instead. New producers are additive; new `kind`s and
  new option verbs are additive (the verb registry gates them).
- The Go binary gains a single-writer attention store and `attention.*` IPC
  methods; the IPC client regen (`make generate-ipc-client`) must run whenever
  they change (CI enforces the generated file is in sync).
- E1 adds exactly two new verbs (`budget.raiseCeiling`, `run.retryWithEscalation`)
  and one IPC wrapper (`issue.removeBlockedBy`); all other options wire to
  existing verbs.
- The hosted platform gains `GET/POST /v1/attention*` and an `attention.event`
  SSE channel; the dashboard renders the inbox and resolves via
  `PendingCommand`. Both are optional: the local store and the extension are a
  complete surface without them.
- Resolutions become first-class audit records in the trace, the run history
  record, and the attention journal — full accountability for every human
  intervention in the fleet.
- The future Discord bot (E2) and Flutter app (E3) are pure `resolve` clients:
  no pipeline change is needed to add a surface, only a new binding to the
  contract in Decision E.

## Implementation tracking

- **#323 (this ADR):** schema, verb registry, transport, resolution semantics,
  surface contract, producers, steer path, audit, alerting model, mockups.
- **#324 / #325:** core primitive (Go single-writer store, verb registry,
  `attention.*` IPC, the two new verbs) + VSCode Action Center tree; producer
  wiring for the initial eight.
- **Hosted platform and dashboard:** the `/v1/attention*` endpoints, the
  `attention.event` SSE channel, `PendingCommand` resolve delivery, and the
  inbox UI are tracked in the closed-source platform repositories. The contract
  they implement is Decision E above; nothing in the local pipeline depends on
  their delivery.
- **Epic #88:** repo-scoped attention — the sweep, standing-condition
  semantics, and the producers that observe a repository with no run in flight.
