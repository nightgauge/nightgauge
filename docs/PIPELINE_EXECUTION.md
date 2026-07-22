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
2. **Startup reconcile** — the orchestrator scheduler calls
   `dockercompose.ListIssueProjects` on startup and tears down projects
   whose worktree no longer exists. Catches leaks from a crashed
   orchestrator that never reached `CleanupWorktree`.
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
