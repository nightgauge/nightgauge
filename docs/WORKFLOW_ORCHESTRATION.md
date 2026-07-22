# Workflow Orchestration — Capability-Routed WorkflowRun Spine

The provider-neutral, CLI-agnostic multi-agent orchestration capability. The
pipeline can fan out subagents, adversarially verify "done" claims with judge
agents, and run codebase-scale work, with **Codex (and the other CLI providers)
as first-class participants** alongside Claude and a **graceful fallback for
every provider**.

This document is authoritative for the shipped engine. Code references in
parentheses point at the merged implementation under
`packages/nightgauge-sdk/src/`.

## Engine owns orchestration

The SDK owns a provider-neutral **workflow engine** that:

- **always** plans the `WorkflowSpec` (`cli/workflow/WorkflowSpec.ts`),
- **always** owns the canonical `WorkflowEvent` tree + durable run-state
  (`cli/workflow/WorkflowEvent.ts`, `orchestrator/WorkflowExecutor.ts`), and
- treats Claude Dynamic Workflows as **one swappable acceleration backend**
  behind an adapter capability.

The portable **`SdkFanoutRunner`** (`cli/workflow/SdkFanoutRunner.ts`) is the
reference contract; the native Claude path **matches it**, not the reverse
(portable-first policy). There is no Claude lock-in: the engine — not the adapter
— owns orchestration, so any provider participates and any provider can fall back
to the floor.

### Architecture at a glance

| Concern                          | Owner                                                                       | Notes                                                                                 |
| -------------------------------- | --------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| Plan the run                     | engine (`selectExecutor` → `WorkflowSpec`)                                  | Always produces a provider-neutral `WorkflowSpec`                                     |
| Canonical event tree + run-state | `WorkflowEvent` contract + `WorkflowExecutor` journal                       | `schemaVersion` 4 node tree; one append-only journal per run                          |
| Reference execution              | `SdkFanoutRunner` (`runSdkFanout`)                                          | Universal floor; defines the contract; hard process/concurrency ceiling               |
| `sdk-fanout` provider execution  | `SdkFanoutExecutors`                                                        | Turns Codex / Gemini / Copilot / LM Studio / Ollama into runner bindings              |
| Accelerated execution            | `adapter.runWorkflow?()`                                                    | Optional offload backend (Claude native Dynamic Workflows, research preview)          |
| Capability declaration           | `OrchestrationCapability` on `ICliAdapter`                                  | `native-workflow` \| `sdk-fanout` (replaced the dead 4-boolean `AdapterCapabilities`) |
| Backend resolution + durability  | `WorkflowExecutor`                                                          | Native vs floor, budget, quota gate, journal, cross-process resume                    |
| Selection / routing              | `PipelineOrchestrator.selectExecutor` + `AutoProviderRouter`                | One selection point; workflow sub-score in the router                                 |
| Anti-hallucination gating        | `GateMetricsWriter.appendJudgeVerdicts` → Go `FeatureValidateGate.Verify()` | Judge verdicts become gate evidence; zero new Go scaffolding                          |

## The `OrchestrationCapability` seam

`ICliAdapter` declares an `OrchestrationCapability` (`native-workflow` |
`sdk-fanout`) via `getOrchestrationCapability()`, plus an optional
`runWorkflow?()` offload hook (`cli/adapters/ICliAdapter.ts`). The two Claude
adapters declare `native-workflow`; Codex / Gemini / Gemini-SDK / Copilot /
LM Studio / Ollama declare `sdk-fanout`. The verified-dead 4-boolean
`AdapterCapabilities` was deleted (no backwards-compat shim — pre-customer
mandate).

The capability is a **declaration of intent**, not a guarantee: a
`native-workflow` adapter still downgrades to the floor at run time when its
version preflight fails (see below), so the seam never blocks a run — it only
selects the fastest available path.

## The canonical `WorkflowEvent` contract

One `schemaVersion: 4` event tree (`cli/workflow/WorkflowEvent.ts`,
`WORKFLOW_SCHEMA_VERSION = 4`). Every backend emits this exact shape and every UI
surface renders it identically. It is an **append-only stream of node emissions**
— each emission is a node's current state; a consumer folds the stream by
`(nodeId, max seq)` into the live tree. Four node kinds form the tree:

```
WorkflowRun (root, carries backend: native-workflow | sdk-fanout)
  └─ WorkflowPhase                          (index / total within the run)
       ├─ SubAgentNode  running → terminal  (one fanned-out agent)
       └─ JudgeVerdict                       (an adversarial judge's verdict)
```

- **`WorkflowRun`** — root node for one orchestrated run; `parentId: null`,
  carries `runId`, optional `issueNumber` / `stage`, and `backend`.
- **`WorkflowPhase`** — a phase within the run (`name`, `index`, `total`).
- **`SubAgentNode`** — one fanned-out agent; carries `provider`, optional
  `model` / `role`, an `outputRef` handle for durable replay, and **REQUIRED**
  per-agent `usage`.
- **`JudgeVerdict`** — an adversarial judge's `verdict`
  (`pass` | `fail` | `uncertain`) on a target node's "done" claim, with optional
  `confidence` / `rationale` and **REQUIRED** `usage` (judges consume budget like
  any agent).

Every node carries `schemaVersion`, `kind`, `nodeId` / `parentId`, a monotonic
`seq`, an ISO-8601 `ts`, and a `status` (`pending` | `running` | `succeeded` |
`failed` | `skipped` | `cancelled`). Leaf nodes also carry a
`WorkflowTerminalKind` (`success` | `error` | `timeout` | `killed` |
`budget-exceeded` | `cancelled`).

**Required usage (no fabricated zeros).** `WorkflowAgentUsage`
(`inputTokens` / `outputTokens` / `cacheReadTokens` / `cacheCreationTokens` /
`costUsd` / `estimated`) is **required** on every terminal agent and judge node
and is populated at emit time. `estimated: true` flags providers that cannot
report real costs — every `sdk-fanout` provider (Codex reports no token usage at
all; the others report tokens but not Claude-grade USD), and any native field
Claude leaves unreported (`mapNativeUsage`). This closed the "zeros +
category:unknown" producer gap (#3914): a failed agent that already burned tokens
carries honest usage and a classified terminal kind rather than zeros + a generic
`error`.

### EventBus is the in-process sink

The flat, Date-stamped `stage:*` `PipelineEvent` union was reworked into a
node-tree `WorkflowEvent` sink — forward-only, the old union deleted (not
aliased). `events/EventBus.ts` **is** a `WorkflowEventSink`: it carries the
`schemaVersion: 4` union, keys subscription on node kind (`run` | `phase` |
`agent` | `judge`) plus an `onAny` firehose, and coalesces high-frequency
`running` agent emissions to ~1 Hz per node so a chatty backend cannot flood the
UI. The six linear pipeline stages are **expressed on this tree** by
`PipelineRunEmitter`: a pipeline run is a `WorkflowRun` whose stages are
first-level `WorkflowPhase` nodes, each driving a single depth-1 `SubAgentNode`
(a single-agent stage is just a depth-1 chain). `PipelineStage` remains a stable
stage-name enum, fully decoupled from the event shape.

## `SdkFanoutRunner` — the reference floor

`runSdkFanout(spec, sink, executor, options)` (`cli/workflow/SdkFanoutRunner.ts`)
is the portable universal floor. It consumes a `WorkflowSpec`, emits the
canonical tree through a `WorkflowEventSink`, and drives provider execution
through **injected** `{ runAgent, runJudge }` bindings (so it is fully
unit-testable with a fake executor and never spawns a real CLI in tests).

- Phases run **sequentially**; within a phase the agents fan out
  **concurrently** under the ceiling, then — after the fan-out barrier — the
  judges run (also under the ceiling).
- A tiny inline concurrency limiter caps in-flight executions with **no npm
  dependency** (deliberate — the hard ceiling is a safety control and must not
  hinge on a third-party package).
- The spec is validated up front and the runner **throws on any problem** rather
  than silently truncating a fan-out.

**Hard process / concurrency ceilings** are a **safety** control, not merely a
budget knob:

| Provider profile      | `maxConcurrent` | `maxTotal` | Constant         |
| --------------------- | --------------- | ---------- | ---------------- |
| Claude                | 16              | 1000       | `CLAUDE_CEILING` |
| Codex / other fan-out | 6               | 32         | `FANOUT_CEILING` |

The runner never runs more than `ceiling.maxConcurrent` agents at once and never
spawns more than `ceiling.maxTotal` over the whole run. The caller-supplied
per-spec `ceiling` is itself clamped to an absolute, **un-overridable**
`ABSOLUTE_CEILING` (16 / 1000, equal to the largest provider ceiling) by
`validateWorkflowSpec` — so a misconfigured or adversarial spec asking for
`maxTotal: 1_000_000` is **rejected**, not honored. Raising `ABSOLUTE_CEILING` is
a deliberate, reviewed code change, never a runtime knob (security review #3916,
finding F1).

### `sdk-fanout` provider execution

`SdkFanoutExecutors` (`cli/workflow/SdkFanoutExecutors.ts`) is the other half of
the floor: it turns an `ICliAdapter` (Codex et al.) into `{ runAgent, runJudge }`
bindings by running **one ephemeral agent per fanned-out unit** — `codex exec
--ephemeral` for Codex, the adapter's own `createQueryFunction(...)` single-shot
exec for Gemini / Copilot / LM Studio / Ollama. Execution is injected behind an
`EphemeralExec` seam so the bindings are unit-testable with a fake exec. Usage is
honest: every record carries `estimated: true`, with real token counts taken from
the provider's result message when reported and left at zero (never invented)
otherwise.

## Native Claude backend — `runWorkflow` offload

`cli/adapters/ClaudeNativeWorkflow.ts` is the shared core behind the optional
`runWorkflow?()` hook for both `ClaudeSdkAdapter` (Agent SDK Dynamic Workflows
builders — `agent()` / `parallel()` / `pipeline()` / `phase()` / `judge()` /
`budget()`) and `ClaudeHeadlessAdapter` (`claude -p` ultracode mode). The engine
— not the adapter — owns orchestration: a native offload still emits the
**canonical `schemaVersion: 4` tree** through the injected sink, with
`WorkflowRun.backend = "native-workflow"` the only difference from the floor's
tree. `emitNativeWorkflowTree` is the sink-shaping half, unit-tested with a fake
native progress source so the tree shape is proven without the native API.

> **Research preview.** The full structure is shipped — the version gate, the
> sink-emitting driver, the typed downgrade signal, and the native →
> `SubAgentNode.usage` mapping. The single concrete native call is marked
> `NATIVE INTEGRATION POINT`; until the Dynamic Workflows API surface ships in
> the pinned Claude binary, `runClaudeNativeWorkflow` throws
> `NativeWorkflowUnavailableError` so the engine deterministically downgrades to
> the floor. It NEVER fabricates a fake API or silently produces a wrong tree.

**Version gate (≥ v2.1.154).** `supportsNativeWorkflow(version)`
(`MIN_NATIVE_WORKFLOW_VERSION = "2.1.154"`) is the exported predicate reused by
the `WorkflowExecutor`. It is **fail-closed** — an unparseable / undetectable
version is treated as below the floor. The headless adapter detects the CLI
version via the injected preflight runner (`claude --version`); the SDK adapter
reads the Agent SDK package version. The ultracode keyword changed from
`workflow` to `ultracode` at v2.1.160 (`ultracodeKeyword`); both are handled in
the `[2.1.154, 2.1.160)` window. `validateAuth` runs a **non-throwing**
`preflightNativeWorkflow` that records a `NativeWorkflowReadiness` verdict — a
stale/disabled workflow version downgrades the orchestration mode to `sdk-fanout`
but **never** hard-fails ordinary (non-orchestrated) execution.

**Graceful downgrade.** When the version floor is unmet, the
`CLAUDE_CODE_DISABLE_WORKFLOWS` env kill-switch is set, the orchestration config
disables the engine, or the native API surface is unavailable (the current
preview state), `runWorkflow` throws the typed `NativeWorkflowUnavailableError`
(carrying a `reason` of `version-below-floor` | `version-undetectable` |
`disabled-by-env` | `disabled-by-config` | `api-surface-unavailable`) and **emits
nothing** — the engine catches it and falls back to the proven `SdkFanoutRunner`
floor.

**Usage.** Native per-agent token/cost figures map onto the REQUIRED
`SubAgentNode.usage` via `mapNativeUsage`: real Claude figures carry through with
`estimated: false`; an absent figure is zero-filled and the record is flagged
`estimated: true` so the cost UI never shows a fabricated exact number.

**Cross-process resume.** `runWorkflow` never delegates resume to Claude's
same-session-only journal — the engine's durable per-node journal (below) is
authoritative.

## `WorkflowExecutor` — backend resolution, budget, journal, resume

`orchestrator/WorkflowExecutor.ts` is the engine that drives one `WorkflowSpec`
end-to-end (sibling to `StageExecutor`). Given a spec, an `ICliAdapter`, and the
resolved orchestration config it:

1. **Resolves the backend** (`resolveBackend`). Native offload is chosen **only**
   when the adapter declares `native-workflow`, actually exposes `runWorkflow`,
   `prefer_native_offload` is on for the run's stage, **and** the version
   preflight passes — otherwise the portable floor (graceful downgrade). The
   default preflight is `DENY_NATIVE_PREFLIGHT` (deny) so the proven floor runs
   until the real preflight is wired. Both paths emit the identical canonical
   tree, so every consumer is backend-agnostic.
2. **Owns budget for both paths.** It honors `budgetUsd` (spec, else config
   `max_usd`) by short-circuiting a spawn once one more agent **would** exceed the
   cap, emitting a deterministic `budget-exceeded` terminal whose usage is zeroed
   so the aggregated `totalCostUsd` can never exceed the cap. It tightens the
   spec's ceiling with config `max_agents` / `max_concurrency` and the
   `ABSOLUTE_CEILING` (`clampSpecCeiling`) — these only ever **lower** the
   provider safety ceiling.
3. **Gates a large fan-out against quota** via `runSdkFanout`'s `quotaProvider`
   seam (see below).
4. **Records nested per-node usage** on an injected `TokenTracker`
   (`recordWorkflowNode`) so a fanned-out run's cost rolls into the pipeline
   totals.
5. **Writes a durable append-only journal** —
   `.nightgauge/pipeline/workflow-{runId}.jsonl`, one `JournalRecord`
   (`{ event, heartbeatMs }`) per emission, via a `JournalingSink` that wraps the
   downstream sink. `resume(runId, spec, sink)` replays the journal
   (`replayJournal`), re-emits the historical tree so a fresh consumer sees the
   whole run, then re-dispatches **only** the nodes that never reached a terminal
   state. Completed agents replay their `outputRef` instead of re-running. A
   node-level liveness heartbeat (`isRunLive`) lets stale-slot recovery
   distinguish a wedged run from one still making progress.

**`outputRef` is untrusted on resume.** A replayed handle is treated as **opaque**
— never `eval`'d, never used as a filesystem path. `sanitizeOutputRef` rejects
anything that is not a bounded (`MAX_OUTPUT_REF_BYTES`), single-line,
non-path-traversing string (no `..`, no absolute/home/UNC/drive-letter paths),
dropping a poisoned handle rather than surfacing it. Malformed final journal
lines (a torn write from a crash) are skipped, not thrown.

All side-effecting seams (clock, filesystem, quota provider, version preflight)
are injected, so the executor is fully unit-testable without real adapters, CLIs,
or disk.

### Quota bridge — Go ratelimit/cooldown → TS gate

The quota/cooldown signals live **only in Go** (`internal/github` /
`internal/gitlab` rate-limit trackers + the autonomous scheduler's dispatch
cooldown). The `workflow.quotaState` IPC method (deterministic, no live probe)
returns the bridged snapshot `{ remaining, limit, resetsAt, cooldownUntil,
cooldownReason, bucket, exhausted }`, where the `exhausted` gate decision is
computed **in Go** so no quota arithmetic is duplicated in TypeScript; the typed
TS client method is regenerated into `IpcClient.generated.ts`.

`WorkflowQuotaGate` (`cli/workflow/WorkflowQuotaGate.ts`) consumes the snapshot
through an injected `QuotaStateProvider` (in VSCode: `ipcClient.workflowQuotaState`).
A fan-out at or above `DEFAULT_LARGE_FANOUT_THRESHOLD` (16 planned agents) into an
`exhausted` quota is **deferred** — zero agents spawned, the run terminates
`skipped`, and a `retryAfter` hint (cooldown deadline, else the GitHub bucket
reset) is surfaced — distinguishing genuine exhaustion from a transient
`status=allowed` stall. A small fan-out is never gated, and the gate **fails
open** (proceeds) if the IPC bridge is unavailable, because the hard concurrency
ceiling remains the unconditional safety control.

## Selection & routing

`PipelineOrchestrator.selectExecutor(stage, issueNumber)`
(`orchestrator/PipelineOrchestrator.ts`) is the **single** selection point — both
`runStage` and `runStageStreaming` route through it, so there is exactly one place
the pipeline branches between the multi-agent `WorkflowExecutor` fan-out and the
unchanged single-agent `StageExecutor`. It returns the single-agent path when:

- orchestration is disabled (`config.disabled`, the default), **or**
- the stage is `pr-create` / `pr-merge` (`ALWAYS_SINGLE_AGENT` — deterministic
  phase nodes, never fanned out), **or**
- the stage SKILL declares no usable `orchestration:` frontmatter, **or**
- no orchestration-capable adapter + bindings are wired
  (`native-workflow → sdk-fanout → single-agent` graceful downgrade bottoms out
  here).

Otherwise it parses the stage SKILL's `orchestration:` frontmatter (the same
skill content `buildStagePrompt` loads) via `parseOrchestrationFrontmatter` into a
single-phase `WorkflowSpec`, folding in run identity, the resolved
`prefer_native_offload[stage]`, and the `max_usd` budget cap. The
`WorkflowExecutor` then clamps the spec's ceiling to the config caps, so even a
synthetic 1000-agent frontmatter cannot exceed the run budget or the hard
provider ceiling. `runStageWorkflow` brackets the fan-out with the stage's own
phase node so the stage still appears as one phase in the canonical tree while its
sub-agent/judge nodes stream through the same sink (the EventBus **is** the sink).

### `orchestration:` skill frontmatter

`parseOrchestrationFrontmatter` (`cli/workflow/parseOrchestrationFrontmatter.ts`)
is the bridge from the skill-author surface to the engine. The block is the
**portable** description of a fan-out — every key is provider-neutral, never a
Claude/Codex tool:

```yaml
orchestration:
  mode: fanout
  phase: verify # phase name (defaults to the stage name)
  ceiling: fanout # fanout (6/32, default) | claude (16/1000)
  units:
    - id: finder
      role: finder
      promptRef: _includes/find.md
      provider: codex # optional pin; omit to let the engine route
    - id: refuter
      role: refuter
      promptRef: _includes/refute.md
  judge: # optional adversarial judge
    mode: merge
    quorum: 1
    promptRef: _includes/judge.md
```

An unknown/missing `ceiling` keyword falls back to the conservative
`FANOUT_CEILING` (never the larger Claude ceiling) so a typo can only narrow a
fan-out. Malformed frontmatter, a missing `orchestration:` block, or zero usable
`units` all resolve to `null` → the stage takes the single-agent path; the prose
body remains the portability floor. The shipped skills carrying the block are
listed in `skills/` (security-audit / feature-dev / feature-validate / audit /
migrate, #3917).

### Router workflow sub-score

`AutoProviderRouter` (`analysis/AutoProviderRouter.ts`) gains a workflow
dimension. For ordinary routing the three classic weights (cost / capability /
context-window) normalise to 1.0 and the workflow sub-score is `0` — byte-for-byte
the pre-existing behaviour. When `AutoRouterContext.requires_workflow` is set, the
router reserves `WORKFLOW_SUBSCORE_WEIGHT` (`0.45`) of the total for the workflow
dimension, scoring each adapter by its declared `OrchestrationCapability`:
`native-workflow → 1.0`, `sdk-fanout → 0.55`. So a native adapter is decisively
preferred for workflow-eligible stages, while a `sdk-fanout` provider (e.g. Codex
via the engine) stays first-class and routable when no native adapter is
authenticated.

## Anti-hallucination gating: judge → gate

Adversarial judge verdicts become **deterministic gate evidence** with zero new
Go scaffolding. `GateMetricsWriter.appendJudgeVerdicts`
(`packages/nightgauge-vscode/src/utils/gateMetricsWriter.ts`) folds the
`JudgeVerdict` nodes of an orchestrated `feature-validate` run into
`.nightgauge/health/gate-metrics.jsonl` as
`{ gate_name: "judges", result }` records, which the existing Go
`FeatureValidateGate.Verify()` loop already consumes. The verdict → result mapping
is **fail-closed**: only an explicit `pass` yields `result: "pass"`; both `fail`
and `uncertain` yield `result: "fail"` (an unconfirmed claim must not slip past
the gate), and the Go gate trips on any `result != "pass"`. A failing record
carries the judge's first-line `rationale` as `error_summary` for triage. The
writer is non-critical (never throws) and validates each record through a Zod
schema before writing.

## Configuration knobs

Orchestration is **off by default** (the engine is opt-in). The knobs live in
three mirrored places — the SDK config (`OrchestrationConfig` in
`cli/workflow/OrchestrationConfig.ts`, surfaced on `PipelineConfig.orchestration`),
the VSCode settings (`nightgauge.orchestration.*` in the extension
`package.json`), and the config manifest (`.nightgauge/config.schema.json` →
`orchestration`).

| Key (`orchestration.*`) | VSCode setting                                 | Type                             | Default | Meaning                                                                                |
| ----------------------- | ---------------------------------------------- | -------------------------------- | ------- | -------------------------------------------------------------------------------------- |
| `disabled`              | `nightgauge.orchestration.disabled`            | boolean                          | `true`  | Disable the engine entirely (off by default).                                          |
| `prefer_native_offload` | `nightgauge.orchestration.preferNativeOffload` | per-stage `{ [stage]: boolean }` | `{}`    | Prefer an adapter's `runWorkflow?()` backend over the portable floor, per stage.       |
| `max_usd`               | `nightgauge.orchestration.maxUsd`              | number (≥ 0)                     | `0`     | Total USD budget for a run. `0` = uncapped. Maps to `WorkflowSpec.budgetUsd`.          |
| `max_agents`            | `nightgauge.orchestration.maxAgents`           | integer (≥ 0)                    | `0`     | Total fan-out cap. `0` = use provider ceiling; `> 0` only lowers `ceiling.maxTotal`.   |
| `max_concurrency`       | `nightgauge.orchestration.maxConcurrency`      | integer (≥ 0)                    | `0`     | Concurrent cap. `0` = use provider ceiling; `> 0` only lowers `ceiling.maxConcurrent`. |

`prefer_native_offload` is keyed by `OrchestrationStage` — every pipeline stage
except the `pipeline-start` / `pipeline-finish` lifecycle markers. The
deterministic `pr-create` / `pr-merge` stages are still valid keys of the type but
are never fanned out regardless, because `selectExecutor` gates them out via
`ALWAYS_SINGLE_AGENT`.

**Resolution & defaults.** `resolveOrchestrationConfig` folds a raw (possibly
unset) block onto `DEFAULT_ORCHESTRATION_CONFIG`, so **reading any knob returns a
concrete value — never `undefined`**. The selection point and the
`WorkflowExecutor` consume the single resolved value
(`PipelineOrchestrator.orchestrationConfig`).

**Kill-switch.** The `CLAUDE_CODE_DISABLE_WORKFLOWS` environment variable forces
`disabled: true` regardless of config (honored by both
`resolveOrchestrationConfig` and the native path).

## Safety & guardrails

- The research-preview native path is **version-gated** (≥ v2.1.154,
  fail-closed) with automatic downgrade to the proven `SdkFanoutRunner` floor.
- **Hard process/concurrency ceilings** (16/1000 Claude, 6/32 fan-out) as a
  safety control. The per-spec `ceiling` is clamped to the un-overridable
  `ABSOLUTE_CEILING` (16/1000) by `validateWorkflowSpec`, so a misconfigured or
  adversarial spec cannot raise it.
- Budget caps in config; budget enforced on **both** backends by the executor.
- Quota-aware large fan-out (defer into an exhausted Go quota; fail-open if the
  bridge is down).
- `pr-create` and `pr-merge` remain **single-agent deterministic phase nodes**
  and are never fanned out (`ALWAYS_SINGLE_AGENT`).
- Replayed `outputRef` is treated as an opaque, sanitized, untrusted handle.
- The full security review of the up-to-1000-process fan-out and native
  workflow-script execution — ceiling enforcement, sandboxed replay,
  prompt-injection / secret-exfil audit — is in
  [docs/security/WORKFLOW_FANOUT_SECURITY.md](security/WORKFLOW_FANOUT_SECURITY.md).

## UI surfaces

The one canonical event tree is the single source every surface renders:

- **VSCode** — the live `workflow → phases → agents → judge` sidebar tree
  subscribes to the SDK EventBus (the in-process `WorkflowEventSink`) and folds
  the node stream into a tree. Because the EventBus expresses even single-agent
  pipeline runs on the same tree (via `PipelineRunEmitter`), the sidebar renders
  ordinary runs and fanned-out runs through one code path.
- **Dashboard** — workflow canvas / phase timeline / fan-out layer / judge rail /
  cost heatmap / budget meter, mounted in the Project Studio shell, off the same
  V4 tree (tracked in the dashboard repo).
- **Flutter** — a `workflow_run` feature (tree list, fan-out groups, judge
  badges, budget meter) consuming the platform's V4 SSE stream (tracked in the
  flutter repo).

## Cross-repo contract flow

The canonical contract is defined **SDK-side first** (`cli/workflow/WorkflowEvent.ts`,
forward-only, no backcompat) so the SDK is not blocked on a platform publish; the
`@nightgauge/shared-types` package re-exports the V4 shape, and the platform
exposes it over a `/v1/workflows` SSE stream that ingests `workflow_nodes`. The
extension consumes the contract via `@nightgauge/shared-types`. Outcome and
learning consumers ingest the V4 nested `agents[]` / `judgeVerdict` shape
(#3915), and the V4 telemetry payload preserves `.strict()` and stays behind the
health-telemetry boundary (see [docs/TELEMETRY_PRIVACY.md](TELEMETRY_PRIVACY.md)).

## No backwards compatibility

Pre-customer: every change here is forward-only — delete old paths, no shims.
Notable reworks: the dead 4-boolean `AdapterCapabilities` was deleted in favour of
`OrchestrationCapability`; the flat `stage:*` EventBus union was reworked to the
node-tree contract; the telemetry/event schema was bumped to V4.

## References

- Epic #3899 — Capability-Routed WorkflowRun Spine
- [docs/security/WORKFLOW_FANOUT_SECURITY.md](security/WORKFLOW_FANOUT_SECURITY.md) — fan-out + native execution security review
- [docs/CONTEXT_ARCHITECTURE.md](CONTEXT_ARCHITECTURE.md) — pipeline context, the node-tree event model
- [docs/PIPELINE_EXECUTION.md](PIPELINE_EXECUTION.md) — execution modes & `selectExecutor` dispatch
- [docs/PIPELINE_STATE_SCHEMA.md](PIPELINE_STATE_SCHEMA.md) — the workflow journal + `workflow_nodes`
- [docs/TELEMETRY_PRIVACY.md](TELEMETRY_PRIVACY.md) — V4 telemetry boundary
- [docs/ARCHITECTURE.md](ARCHITECTURE.md) — product layers & the workflow engine
- [docs/SETTINGS_ARCHITECTURE.md](SETTINGS_ARCHITECTURE.md) — `orchestration.*` knob tiering
