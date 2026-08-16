# Pipeline Execution Modes

This document describes the two execution modes for the Nightgauge
pipeline: **Manual Mode** (CLI) and **Automated Mode** (VSCode Extension).

## Scope

This document covers **where** you run the pipeline — CLI vs VSCode Extension.
It explains the differences between manual stage invocation and automated
orchestration, including context file inspection and stage resumption.

For **how** the VSCode extension handles process lifecycles, stdin/stdout
management, and interactive vs headless execution within the extension, see
[INTERACTIVE_MODE.md](./INTERACTIVE_MODE.md).

## Overview

The Nightgauge pipeline can be executed in two ways, depending on your
environment and preferences:

| Mode      | Environment      | Control Level | Best For                          |
| --------- | ---------------- | ------------- | --------------------------------- |
| Manual    | Any Claude Code  | Full control  | Learning, debugging, custom flows |
| Automated | VSCode Extension | Guided        | Production workflows, consistency |

Both modes use the same underlying skills and produce identical context files,
ensuring consistent behavior regardless of execution method.

---

## Manual Mode (CLI)

Run each pipeline stage individually via Claude Code CLI. This mode provides
full control over each stage with explicit approval gates.

### Execution Flow

```bash
# 1. Claim issue and create branch
/nightgauge:issue-pickup 42

# 2. Plan the implementation (approval gate: review PLAN.md)
/nightgauge:feature-planning

# 3. Implement the feature (no commit/push — code stays on disk)
/nightgauge:feature-dev

# 4. Validate, then commit+push only validated code
/nightgauge:feature-validate

# 5. Create pull request
/nightgauge:pr-create

# 6. Wait for reviews and merge
/nightgauge:pr-merge
```

### Manual Mode Benefits

- **Full control**: Run stages in any order, skip optional stages
- **Debugging**: Inspect context files between stages
- **Learning**: Understand how each stage works
- **Portability**: Works anywhere Claude Code runs (terminal, IDE, remote)

### Context File Inspection

Between stages, you can inspect the handoff files:

```bash
# View issue context
cat .nightgauge/pipeline/issue-42.json | jq

# View planning decisions
cat .nightgauge/pipeline/planning-42.json | jq '.decisions'

# View implementation summary (commit_sha is null — commit happens in validate)
cat .nightgauge/pipeline/dev-42.json | jq '.files_changed'

# View validation results and commit SHA
cat .nightgauge/pipeline/validate-42.json | jq '.commit_sha'
```

### Stage Resumption

If a stage fails or you need to restart:

```bash
# Re-run a specific stage (reads previous context, overwrites its output)
/nightgauge:feature-dev

# Or start from a specific point with explicit context
/nightgauge:pr-create  # Reads dev-42.json or validate-42.json
```

---

## Automated Mode (VSCode Extension)

Use the Nightgauge VSCode extension for end-to-end orchestration with
progress visualization, token tracking, and crash recovery.

### Prerequisites

1. Install the Nightgauge VSCode Extension
2. Ensure Claude CLI is installed and authenticated
3. Open a repository with `.nightgauge/config.yaml` configuration

### Execution Flow

1. Open Command Palette: `Cmd+Shift+P` (Mac) or `Ctrl+Shift+P` (Windows/Linux)
2. Run: `Nightgauge: Run Pipeline`
3. Enter issue number when prompted
4. Pipeline executes with approval gates at key stages

### Automated Mode Features

| Feature              | Description                                                   |
| -------------------- | ------------------------------------------------------------- |
| **Progress Sidebar** | Visual stage status with running/complete/failed indicators   |
| **Workflow Tree**    | Live `run → phase → agent → judge` node tree off the EventBus |
| **Token Dashboard**  | Real-time token usage, cost tracking, efficiency metrics      |
| **Crash Recovery**   | Resume interrupted pipelines (1-hour timeout)                 |
| **Session History**  | Track runs across sessions, compare efficiency over time      |
| **Approval Gates**   | Pause for user approval at planning and PR creation stages    |

### Live Workflow Tree (Issue #3919)

The **Workflow** sidebar view renders the canonical `schemaVersion-4`
`WorkflowEvent` node tree — `WorkflowRun → WorkflowPhase → SubAgentNode →
JudgeVerdict` — DIRECTLY off the SDK EventBus (the in-process
`WorkflowEventSink`), re-served over SSE by the platform. There is no local event
mirror: `EventStreamService` validates every emission with one
`parseWorkflowEvent` Zod call and forwards the node verbatim — `nodeId` /
`parentId` / `seq` / `ts` intact. `WorkflowTreeProvider` folds the append-only
stream by `(nodeId, last-write-wins seq)` into the live hierarchy.

Each row surfaces:

- **Status dots** per node (pending / running / succeeded / failed / skipped).
- **Per-agent token + cost** in the description, plus full usage in the tooltip.
- **Judge badges** — green pass / red fail / yellow uncertain — with the
  rationale in the tooltip.
- **Fan-out counter** on the run row (e.g. `7/7 agents, 2 rejected`).
- **Lanes-busy gauge** (e.g. `N of 16 lanes busy`).

**Honesty rules.** The gauge ceiling is the real per-backend lower bound — 16
lanes for the Claude `native-workflow` offload, 6 lanes for the portable
`sdk-fanout` floor. On `sdk-fanout` runs costs are labelled estimates and judges
are labelled "gate verification" (deterministic post-condition checks, not
adversarial judgements); `native-workflow` runs are labelled research-preview.
This reverses the flat `pipeline.*` event mirror from #3714.

### VSCode Commands

| Command                           | Description                         |
| --------------------------------- | ----------------------------------- |
| `Nightgauge: Run Pipeline`        | Start full pipeline for an issue    |
| `Nightgauge: Run Stage`           | Run a specific stage only           |
| `Nightgauge: View Dashboard`      | Open token/cost analytics dashboard |
| `Nightgauge: View Pipeline State` | Show current state.json             |
| `Nightgauge: Cancel Pipeline`     | Stop running pipeline               |

### HeadlessOrchestrator

The automated mode uses `HeadlessOrchestrator` which:

1. Spawns Claude CLI in headless mode (`--output-format stream-json`)
2. Parses token usage from stdout
3. Updates `PipelineStateService` with progress
4. Emits events for UI components to subscribe to

---

## Execution Architecture

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                         EXECUTION MODE COMPARISON                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  MANUAL MODE                           AUTOMATED MODE                        │
│  ───────────                           ──────────────                        │
│                                                                              │
│  User → Claude Code CLI                User → VSCode UI                      │
│           │                                     │                            │
│           │ runs                                │ triggers                   │
│           ▼                                     ▼                            │
│  ┌─────────────────┐                  ┌─────────────────────┐               │
│  │ Pipeline Skill  │                  │ HeadlessOrchestrator │               │
│  │ (SKILL.md)      │                  │                     │               │
│  └────────┬────────┘                  └──────────┬──────────┘               │
│           │                                      │                           │
│           │ writes                               │ spawns                    │
│           ▼                                      ▼                           │
│  ┌─────────────────┐                  ┌─────────────────────┐               │
│  │ Context Files   │◄────────────────►│ Claude CLI          │               │
│  │ (.claude/       │                  │ (--output-format    │               │
│  │  context/*.json)│                  │  stream-json)       │               │
│  └────────┬────────┘                  └──────────┬──────────┘               │
│           │                                      │                           │
│           │                                      │ parses stdout             │
│           │                                      ▼                           │
│           │                           ┌─────────────────────┐               │
│           │                           │ PipelineStateService│               │
│           │                           │ (state.json)│              │
│           │                           └──────────┬──────────┘               │
│           │                                      │                           │
│           │                                      │ emits events              │
│           │                                      ▼                           │
│           │                           ┌─────────────────────┐               │
│           │                           │ UI Components       │               │
│           │                           │ • TreeProvider      │               │
│           │                           │ • Dashboard         │               │
│           │                           │ • OutputWindow      │               │
│           │                           └─────────────────────┘               │
│           │                                                                  │
│           ▼                                      ▼                           │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    SAME CONTEXT FILES                                │   │
│  │  .nightgauge/pipeline/issue-42.json                                       │   │
│  │  .nightgauge/pipeline/planning-42.json                                    │   │
│  │  .nightgauge/pipeline/dev-42.json      (commit_sha=null)                  │   │
│  │  .nightgauge/pipeline/validate-42.json (commit_sha set after validation) │   │
│  │  .nightgauge/plans/42-feature-description.md                             │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Who Resolves the Model (Issue #340)

Model resolution has **exactly one owner per dispatch path**. Two independent
resolvers producing the same decision is the Dual-Path Drift class in
[FAILURE_TAXONOMY.md](FAILURE_TAXONOMY.md), and on the IPC path it was a live
defect: the escalated tier was computed, logged, and recorded in run history,
while the CLI kept spawning on the tier that had just failed.

| Dispatch path                                   | Resolver                                 | What it applies                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ----------------------------------------------- | ---------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Go scheduler → IPC → extension** (autonomous) | `scheduler.resolveDispatchModel` (Go)    | performance-mode pin **and envelope**, `pipeline.stage_models` + `NIGHTGAUGE_PIPELINE_STAGE_MODEL_*`, `model_routing.mode`, lightweight stage defaults, the run's routed tier, `ui.core.default_model` + `NIGHTGAUGE_UI_CORE_DEFAULT_MODEL` (Step 3), post-failure escalation, sticky model-unavailable downgrades (#42), `model_routing.minimum_model` (#366), pr-create large-diff, feature-validate haiku gate, pr-merge haiku floor (#197)                          |
| **Go scheduler → ExecutionManager** (auto/CLI)  | `scheduler.resolveDispatchModel` (Go)    | same — this is why both paths agree                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| **HeadlessOrchestrator** (extension-driven)     | `resolveModel` in `utils/skillRunner.ts` | performance-mode pins **and envelope**, `pipeline.stage_models` + env, `model_routing.mode`, lightweight stage defaults, `model_routing.minimum_model`, `ui.core.default_model` (Step 3), pr-create large-diff — plus three the Go resolver has no counterpart for: the adaptive policy override (Step 1.6), A/B experiment assignment (Step 1.7) and `AutoModelSelector` (Step 2). Escalation and sticky downgrades belong to whichever orchestrator drives this path. |

On the IPC path the extension **executes** the wire model verbatim: it passes
`RunStageParams.model` to `runStageSkillHeadless` as the authoritative
`modelOverride`, which skips the local chain entirely. A `pipeline.runStage`
event with no model fails the stage with an `[ipc-contract]` error rather than
resolving a second answer.

Everything in the third row that the Go resolver needed in order to be a
complete router lives in
[`internal/orchestrator/dispatch_routing.go`](../internal/orchestrator/dispatch_routing.go),
applied by `stageBaseModel` before escalation and the floors. Its tables are
deliberate duplicates of named TypeScript counterparts
(`getStageModel`/`DEFAULT_STAGE_MODELS`, `LIGHTWEIGHT_STAGE_DEFAULTS`,
`getDefaultModel`), each annotated with its pair — threading TS config over the
wire would put the extension back in the routing business, which is the drift
#340 removed.

The performance mode is the exception: it is not duplicated in that file. Both
Go callers — the router's recommendation at pickup and this per-stage
resolution — read one table, `routing.modeProfiles`
([`internal/intelligence/routing/performance_mode.go`](../internal/intelligence/routing/performance_mode.go)),
which mirrors `MODE_PROFILES`
(`packages/nightgauge-vscode/src/utils/modeProfiles.ts`) including the part that
is easy to get wrong: **only `maximum` pins.** Since #19, `efficiency` and
`frontier` carry empty `stages` maps and express themselves as `[floor,
ceiling]` envelopes instead.

**The mode envelope on the Go path**, stated because three of its rules are
decisions rather than translations:

- The **ceiling binds everything the pipeline chose** — the routed tier, the
  lightweight defaults, `ui.core.default_model`, post-failure escalation, the
  `minimum_model` floor and the `run.retryWithEscalation` forced tier. A
  cost-capping mode that any later mechanism can raise out of caps nothing.
  `resolveModel` applies the same rule to its own `enforceMinimumModel` result.
  The clamp is applied to **what the raising mechanisms produced**, never
  skipped on the provenance of the base they raised — gating it on "the base was
  explicit" makes it a no-op under `model_routing.mode: manual`, where the
  explicit chain answers for _every_ stage.
- An **explicit per-stage model is not clamped** (`pipeline.stage_models`,
  `NIGHTGAUGE_PIPELINE_STAGE_MODEL_*`, and the manual-mode table): that is the
  operator overriding the mode for one stage, and `resolveModel` Step 1 returns
  it unclamped too. The env override is read ahead of the Maximum pin, on both
  resolvers, so it wins in every mode. The exemption covers the operator's own
  value and only it: a floor still binds that value, the ceiling still binds the
  raise, and the result is never **below** what the operator named — forcing a
  tier must not downgrade.
- The **envelope floor is never re-applied after the sticky #42 downgrade**, or
  a Maximum-mode run whose API rejection lowered the tier would be forced
  straight back onto the rejected one.
- The band a pipeline-chosen tier is clamped into is the stage's **routed-tier
  envelope** (`routing.RoutedTierEnvelope` / `getRoutedTierEnvelope`), not the
  raw mode band: a `fable` ceiling is offered only to the heavy reasoning stages
  (`feature-planning`, `feature-dev`), mirroring `AutoModelSelector`'s own
  frontier-reasoning rule. Every other stage — `feature-validate` included,
  which `MODE_PROFILES.frontier` documents as capped at Opus — tops out at Opus,
  **including** when a `minimum_model` floor or a forced tier is what named
  Fable. Both resolvers narrow, for different reasons: Go applies ONE routed
  tier (the feature-dev recommendation from `issue-{N}.json`) to every stage, and
  TypeScript's floor arrives after the per-stage selector has already made its
  own pick.

Stated consequence, so it is not silent: three things have **no** Go
counterpart, and are therefore not consulted on an autonomous run — the
adaptive-policy override (Step 1.6), the A/B experiment assignment (Step 1.7),
and `AutoModelSelector` (Step 2 — Go routes from the issue's complexity score at
pickup instead; its frontier-reasoning escalation is mirrored in `routeLocal`).
Step 3, the global `ui.core.default_model` / `NIGHTGAUGE_UI_CORE_DEFAULT_MODEL`
fallback, is **not** on that list: it is mirrored by `workspaceDefaultModel`
(`dispatch_routing.go`). It had to be, because pre-#340 it was the _effective_
model for every reasoning stage on the IPC path — `services/SkillRunner.ts`
passed no issue metadata, so Step 2 never fired and Step 3 always won.

And the reverse, stated for the same reason: two **post-base** mechanisms exist
only in `resolveDispatchModel`, so the two resolvers deliberately disagree
where they fire — the `feature-validate` haiku gate (#3041: haiku shortcuts
real build/test commands, so the stage escalates to sonnet unless the dev
stage's build verification passed) and the `pr-merge` haiku floor (#197). Both
raise a haiku result to sonnet on the Go path only. Post-failure escalation and
the sticky model-unavailable downgrade (#42) are likewise Go's, and belong to
whichever orchestrator drives the run.

The `pr-create` large-diff escalation is **not** a third one — it is shared, and
it fires under the same condition on both resolvers: **only over a base the
pipeline chose.** In `resolveModel` that is structural (it lives inside the Step
1.5 lightweight branch, which Step 1 returns before ever reaching); in
`resolveDispatchModel` it is an explicit guard on `stageBaseModel`'s `explicit`
flag. Without the guard it also fired over an explicit `pipeline.stage_models`
entry, a `NIGHTGAUGE_PIPELINE_STAGE_MODEL_*` override and the whole
`model_routing.mode: manual` table — where `defaultStageModels[pr-create]` is
haiku and all three recommended `CONFIGURATION.md` profiles live — so one
workspace and one 900-line diff ran `pr-create` on sonnet autonomously and haiku
from the extension. Explicit operator configuration wins; the escalation exists
to stop the pipeline's own cheap default from stalling, not to overrule a
choice.

The mode × knob matrix both resolvers must agree on is asserted twice, once per
resolver: `TestDispatchModelModeKnobMatrix`
(`internal/orchestrator/dispatch_routing_mode_test.go`) and
`resolveModel.modeKnobAgreement.test.ts`.

The visible routing delta on the IPC path, in the default `automatic` +
`elevated` configuration:

- **`feature-planning` / `feature-dev` / `feature-validate`** now dispatch on
  the run's complexity-routed tier from `issue-{N}.json`. Previously they got
  the global default, because the IPC path never passes `issueMetadata` and
  `AutoModelSelector` therefore never ran.
- **`issue-pickup` / `pr-create`** keep the built-in lightweight `haiku` base
  (still raised by the pr-create large-diff escalation, the `minimum_model`
  floor and post-failure escalation). `pr-merge` has no lightweight base — #197
  removed it, because the pr-merge LLM path runs only when the deterministic
  runner punted, i.e. exclusively on the hardest merges — so it dispatches the
  run tier, floored at sonnet.

Two things the extension still owns on the IPC path, because Go resolves
neither:

- **Adapter model translation.** Go speaks one vocabulary: the registry tier
  band its ladders are built on (`haiku|sonnet|opus|fable`), guaranteed by
  `normalizeDispatchTier` as `resolveDispatchModel`'s last step. (A
  user-configured local model the registry does not know has no band and passes
  through as itself.) Non-Claude CLIs need a provider-specific id, and only the
  extension knows which adapter it selected, so the translation happens at the
  last mile. The model the adapter process was actually spawned with is reported
  back as `servedModel`, so run history attributes — and prices — the model that
  ran rather than the tier that was asked for.

  On both extension dispatch paths, `codex`, `gemini`, `gemini-sdk` and
  `copilot` translate every recognized dispatched band in every performance
  mode. Codex uses `resolveCodexPipelineModel`; the other adapters use the
  registry-backed `getAdapterModelForBand`, which is the same mapping primitive
  that `getModeStageAdapterModel` uses for Maximum-mode pins. This is a
  last-mile translation, not a second routing decision: escalation, floors and
  sticky downgrades have already selected the band.

  If an adapter has no registry mapping for a recognized band, the extension
  launches its configured adapter model and records the source as `config`
  rather than passing an unsupported alias. `lm-studio` remains the deliberate
  exception (#3214): local model catalogs have no registry tier hierarchy, so
  every band takes that fallback and the loaded local model serves the run.
  The agentic-adapter gate currently bars `gemini-sdk` and `lm-studio` from
  pipeline-stage execution (#57); their mapping/fallback rules still govern
  extension surfaces where those adapters are eligible.
  The Go executor's adapters (`internal/execution/adapters`, the
  `nightgauge run` path) use the same remote-provider mappings; local adapters
  likewise do not invent a tier hierarchy.

  `servedModel` therefore carries a CONCRETE id, deliberately, and the two
  questions asked about it live in one space each. "Did the process run what the
  adapter was asked to launch?" is answered in concrete-id space, in the
  extension. "Did the run serve the band the router predicted?" is answered in
  band space, in Go, by `OutcomeActualBand`
  ([`internal/orchestrator/outcome_semantics.go`](../internal/orchestrator/outcome_semantics.go)),
  which inverts the adapter mapping instead of collapsing a multi-band id onto
  its strongest band — `gpt-5.6-sol` serves both `opus` and `fable`, so the
  collapse booked every correctly-served codex run as a routing miss. See
  [OUTCOME_RECORDING.md](OUTCOME_RECORDING.md).

- **Effort.** The wire carries it since #581 (spike #568 §4.1: the dispatch
  envelope is `(model, effort, thinking)`). On the IPC path the Go scheduler
  resolves the stage's effort with `resolveWireEffort`
  ([`internal/orchestrator/dispatch_envelope.go`](../internal/orchestrator/dispatch_envelope.go)),
  a step-for-step mirror of the TS `resolveStageEffort` chain (the mode's
  effort pin and `[effortFloor, effortCeiling]` envelope,
  `model_routing.stage_efforts` + env overrides, `default_effort`, the
  manual-mode table), puts it on `RunStageParams.effort`, and the extension
  executes it verbatim — precedence moved to the wire without changing any
  outcome, and the twin matrix (Go `TestResolveWireEffort` ⇄ TS
  `resolveModel.modeKnobAgreement.test.ts`) pins the mirror. An absent wire
  effort means "no explicit effort resolved anywhere": the extension omits
  `--effort` and the model's declared default rules, exactly as before. On the
  HeadlessOrchestrator path `resolveStageEffort` still resolves locally —
  each resolver owns one path, as with the model. Whether the model accepts
  `--effort` at all is a question about its tier band, so the extension
  normalizes the value with `modelTierBand` before asking — a concrete id
  would otherwise drop the flag with no error and no log line. The grok
  adapters remain the deliberate exception: their effort is the
  provider-global `NIGHTGAUGE_GROK_EFFORT` env contract (#569) on both paths.

- **Thinking.** Also on the wire since #581, as attribution rather than
  execution (no CLI flag exists): `resolveWireThinking` answers with the
  selection query's declared rung for the dispatched band
  (`routing.ResolveBandEnvelope`) under the anthropic band contract the
  `model` field already speaks, `off` when the `CLAUDE_CODE_DISABLE_THINKING`
  interlock is set, and absent when undeclared. Run records carry the wire
  value; a served-thinking correction (the `servedModel` analogue) does not
  exist yet.

- **Eval-advice policy differs per resolver — deliberately, and only when
  `model_routing.use_eval_recommendations` is ON.** The TS resolver consumes
  advice only for issues whose `type:` label directly names an eval job class
  (docs/bug/refactor), from exact job-class entries with exact-over-model
  backoff (`pickAdvice`); the Go dispatch path has no job-class attribution,
  so it consumes at spike §4.3's `(model, *, *)` backoff level, pooling
  advisable entries across job classes (`routing.AdviseBand`). Identical
  inputs can therefore resolve differently on the IPC vs Headless paths while
  the key is on. This is a documented, opt-in asymmetry, not drift-by-neglect
  — the convergence plan (job-class attribution on the Go dispatch path) is
  tracked in the #581 follow-up.

Attribution follows the same ownership. Go records the dispatch model up front
(`runtime.RecordStageModel` at stage start) and re-records on the served model
the extension reports at completion. The `pipeline.notifyStageTransition`
up-front attribution (#367) is deliberately **not** wired on the IPC path: that
handler keys `activeRuntimes` by issue number for HeadlessOrchestrator-initiated
runs and would mint a second, competing runtime for a Go-scheduled one.

---

## Per-Stage Executor Dispatch (single-agent vs. fan-out)

Independent of the manual/automated **mode** above, each stage is dispatched to
one of two **executors** at `PipelineOrchestrator.selectExecutor(stage,
issueNumber)` — the single point both `runStage` and `runStageStreaming` route
through:

- **Single-agent (`StageExecutor`)** — the default and only path when
  orchestration is disabled (the default), for the deterministic `pr-create` /
  `pr-merge` stages, when the stage SKILL declares no usable `orchestration:`
  frontmatter, or when no orchestration-capable adapter is wired.
- **Multi-agent fan-out (`WorkflowExecutor`)** — when an enabled stage's SKILL
  declares an `orchestration:` block. The block compiles into a `WorkflowSpec`
  and the executor resolves a backend: a Claude **native-workflow** offload when
  the adapter declares it, exposes `runWorkflow`, `prefer_native_offload` is on
  for the stage, and the version preflight (≥ v2.1.154) passes — otherwise the
  portable **`sdk-fanout`** floor (Codex / Gemini / Copilot / LM Studio /
  Ollama). The chain `native-workflow → sdk-fanout → single-agent` degrades
  gracefully, so a workflow-eligible stage never hard-fails for lack of a
  backend.

Both executors emit the same canonical `WorkflowEvent` node tree, so the live
sidebar tree, token tracking, and the durable journal are backend-agnostic. See
[docs/WORKFLOW_ORCHESTRATION.md](WORKFLOW_ORCHESTRATION.md) for the full design.

---

## Token Tracking by Mode

| Aspect            | Manual Mode                | Automated Mode                    |
| ----------------- | -------------------------- | --------------------------------- |
| **Real-time**     | Claude Code shows usage    | Dashboard + sidebar display       |
| **Per-stage**     | Not tracked persistently   | Stored in state.json              |
| **Historical**    | Not available              | Last 50 runs in workspace storage |
| **Cost analysis** | Per-session in Claude Code | ROI metrics, efficiency trends    |

---

## Choosing an Execution Mode

### Use Manual Mode When

- Learning how the pipeline works
- Debugging a specific stage
- Running on a machine without VSCode
- Need custom stage ordering or conditional execution
- Working remotely via SSH/terminal

### Use Automated Mode When

- Running production workflows consistently
- Need token tracking and cost analysis
- Want crash recovery for long-running pipelines
- Prefer visual progress indicators
- Building team dashboards for pipeline metrics

---

## Mixing Modes

The modes are interoperable. You can:

1. **Start in automated mode, continue manually**:
   - Run `Nightgauge: Run Pipeline` through feature-planning
   - Cancel the automated run
   - Continue with `/nightgauge:feature-dev` in terminal

2. **Start manually, switch to automated**:
   - Run `/nightgauge:issue-pickup 42` and
     `/nightgauge:feature-planning`
   - Open VSCode, run `Nightgauge: Run Stage` → `feature-dev`
   - Automated mode picks up from existing context files

3. **Monitor manual runs in VSCode**:
   - Run stages in terminal
   - VSCode's ContextWatcherService detects running files
   - TreeProvider shows status in sidebar

---

## Integration-Test Strict Gate (Issue #2909)

The `feature-validate` stage enforces a **strict integration-test gate** by
default. The pipeline's core contract is "validate → publish green" — a
validate stage that silently passes when integration tests can't be run
violates that contract and produces queues of unmergeable PRs.

**Rule**: if CI declares integration tests (via a `test:integration` npm
script, a CI workflow step whose name or command contains "integration", or a
`tests/integration/` directory), `feature-validate` must actually execute them
locally. Environmental failures (docker daemon unavailable, postgres
unreachable, missing env vars, etc.) are treated as **stage failures**, not
environmental passes.

**Modes** (`validation.integration_tests`):

| Mode          | Behavior                                                                                                                                                        |
| ------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `strict`      | Default. A required integration suite that did not run fails the stage with `validation_status: "failed"`. No PR is created.                                    |
| `best_effort` | Legacy pre-#2909 behavior. Attempts to run, records a warning if services are unavailable, allows PR creation to proceed. Use only when you explicitly want it. |
| `off`         | Skip the integration-test gate entirely. For repos that intentionally do not run integration tests locally.                                                     |

**Classification**: the gate distinguishes _environmental_ failures (docker
daemon, postgres, redis, DNS, missing env vars) from real test failures by
matching the command's stdout/stderr against a conservative pattern list in
`IntegrationTestGate.ts`. A genuine assertion failure is always treated as
`validation_status: "failed"`, regardless of mode.

**Extending**: add new environmental patterns to
`ENVIRONMENTAL_FAILURE_SIGNALS` in
[`packages/nightgauge-sdk/src/tools/integration-test-gate/IntegrationTestGate.ts`](../packages/nightgauge-sdk/src/tools/integration-test-gate/IntegrationTestGate.ts)
when a real CI vs. local divergence surfaces a pattern we're missing. The list
is intentionally conservative — false positives let broken tests masquerade
as environment problems.

## Per-Issue Docker Compose Stacks

When a pipeline stage spins up a Docker Compose stack for adapter or E2E
tests, the compose project name is **always** `issue-<number>` (no repo
prefix). This contract is enforced by both the worktree teardown path and
the `nightgauge cleanup` operator command.

### Naming contract

| Resource             | Name shape                 | Example                   |
| -------------------- | -------------------------- | ------------------------- |
| Compose project      | `issue-<number>`           | `issue-836`               |
| Containers           | `issue-<number>-<svc>-<n>` | `issue-836-api-1`         |
| Named volumes        | `issue-<number>_<vol>`     | `issue-836_postgres_data` |
| Networks             | `issue-<number>_<net>`     | `issue-836_default`       |
| Locally-built images | `issue-<number>-<svc>`     | `issue-836-api`           |

Producers (the pipeline adapter / E2E flow that runs
`docker compose -p issue-<number> up`) MUST use this exact project name. A
single host running pipelines for multiple repos cannot have two
`issue-<N>` worktrees with the same number concurrently — the pipeline
serializes per issue, and concurrent runs across repos with the same issue
number are vanishingly rare.

### Teardown lifecycle

Three layers protect against leaked compose state:

1. **Per-teardown** — `CleanupWorktree` (Go) and `WorktreeManager.cleanup`
   (TS) run `docker compose -p issue-<N> down -v --remove-orphans` and
   remove project-tagged images BEFORE removing the worktree directory.
   Soft-fail: docker missing or daemon down logs a warning and proceeds.
2. **Reconcile pass** — the autonomous scheduler
   (`sweepOrphanedComposeProjects`) calls `dockercompose.ListIssueProjects` on
   its reconcile cycle, under the same graph-TTL pacing gate as the other
   sweeps, and tears down projects with **no run in flight**. The in-flight
   set is a union — `state.Running`, the active-worktree scan, and the
   per-root runtime-snapshot scan — and an **undetermined** read of any of
   them vetoes the whole pass rather than shrinking the protected set. It does
   **not** run at scheduler construction: `queue list` used to tear down
   containers as a side effect of building a Scheduler (#403/#410). See
   [docs/GO_BINARY.md](GO_BINARY.md#compose-orphan-reconciliation-issue-3050).
   Catches leaks from a crashed orchestrator that never reached
   `CleanupWorktree`.
3. **Operator escape hatch** — `nightgauge cleanup` lists or tears
   down leaked projects manually:

   ```bash
   nightgauge cleanup                # tear down orphans only (default)
   nightgauge cleanup --dry-run      # preview without acting
   nightgauge cleanup --all          # tear down every issue-* project
   nightgauge cleanup --json         # machine-readable output
   ```

   `nightgauge doctor` also reports orphans as warnings so they
   surface during routine environment checks.

CI runs an additional pre-E2E cleanup step on the self-hosted runner that
combines `nightgauge cleanup --all` with raw `docker rm -f` /
`docker volume rm` / `docker network rm` / `docker rmi -f` filtered to
`name=issue-` for defense-in-depth.

## Related Documentation

- [ISSUE_TO_PR_WORKFLOW.md](ISSUE_TO_PR_WORKFLOW.md) - Complete pipeline
  workflow
- [CONTEXT_ARCHITECTURE.md](CONTEXT_ARCHITECTURE.md) - Context file schemas
- [ARCHITECTURE_DIAGRAMS.md](ARCHITECTURE_DIAGRAMS.md) - Visual architecture
  diagrams

## Author

nightgauge
