# @nightgauge/sdk

**Nightgauge SDK** - Programmatic pipeline orchestration using the
Claude Agent SDK.

## Installation

The SDK is **not yet published to npm** — build it from a clone of this
repository:

```bash
git clone https://github.com/nightgauge/nightgauge.git
cd nightgauge && npm install
npm run -w @nightgauge/sdk build
```

The CLI entry point is then available as
`node packages/nightgauge-sdk/dist/cli/index.js` (or via
`npx nightgauge-sdk` inside this workspace).

## Quick Start

### CLI Usage (CI/CD)

```bash
# Run full pipeline for an issue
npx nightgauge-sdk run 42

# Run with auto-approve for CI
npx nightgauge-sdk run 42 --auto-approve --format json

# Run a single stage
npx nightgauge-sdk stage feature-planning 42

# Check pipeline status
npx nightgauge-sdk status 42
```

**Environment Variables:**

```bash
export NIGHTGAUGE_ADAPTER=claude-headless # claude-sdk | claude-headless | codex | gemini | gemini-sdk
# Required only for claude-sdk adapter:
export ANTHROPIC_API_KEY=sk-ant-...
# Required only for gemini-sdk adapter:
export GEMINI_API_KEY=...              # or GOOGLE_API_KEY
export NIGHTGAUGE_GEMINI_MODEL=gemini-2.5-flash  # Optional model override
export NIGHTGAUGE_AUTO_APPROVE=true      # Skip approval prompts
export NIGHTGAUGE_OUTPUT_FORMAT=json     # JSON output for parsing
export NIGHTGAUGE_TIMEOUT=3600000        # Global timeout (1 hour)
export NIGHTGAUGE_STAGE_TIMEOUT=900000   # Stage timeout (15 min)
```

See [docs/CI_INTEGRATION.md](../../docs/CI_INTEGRATION.md) for complete CLI
documentation.

### Programmatic Usage

Direct Agent SDK integration is opt-in because Nightgauge does not redistribute
Anthropic's Agent SDK. Install it separately after reviewing Anthropic's license
and commercial terms:

```bash
npm install @nightgauge/sdk @anthropic-ai/claude-agent-sdk
```

```typescript
import { query } from "@anthropic-ai/claude-agent-sdk";
import { PipelineOrchestrator } from "@nightgauge/sdk";

// Create orchestrator with Agent SDK query function
const orchestrator = new PipelineOrchestrator(query, {
  autoApprove: true, // Skip approval prompts (CI mode)
  globalTimeoutMs: 3600000, // 1 hour timeout
  stageTimeoutMs: 900000, // 15 min per stage
});

// Subscribe to events
orchestrator.events.on("stage:complete", (event) => {
  console.log(`Completed ${event.stage} in ${event.durationMs}ms`);
});

orchestrator.events.on("approval:needed", (event) => {
  console.log(`Approval required for ${event.stage}: ${event.reason}`);
});

orchestrator.events.on("stage:timeout", (event) => {
  console.log(`Stage ${event.stage} timed out after ${event.timeoutMs}ms`);
});

// Run complete pipeline for an issue
const result = await orchestrator.run(42);
console.log(`Pipeline complete! Total cost: $${result.usage.costUsd.toFixed(4)}`);
```

## API Overview

### PipelineOrchestrator

Main class for running the full Issue-to-PR pipeline.

```typescript
const orchestrator = new PipelineOrchestrator(query, {
  cwd: "/path/to/repo", // Optional, defaults to process.cwd()
  defaultModel: "sonnet", // Optional: sonnet, opus, haiku
  autoApprove: false, // Skip approval prompts (CI mode)
  globalTimeoutMs: 3600000, // Pipeline timeout (1 hour)
  stageTimeoutMs: 900000, // Per-stage timeout (15 min)
});

// Run full pipeline
const result = await orchestrator.run(issueNumber);

// Run individual stage
const stageResult = await orchestrator.runStage("feature-planning", issueNumber);

// Stop running pipeline
await orchestrator.stop();
```

### StageExecutor

Low-level executor for individual pipeline stages.

```typescript
import { StageExecutor, buildStagePrompt } from "@nightgauge/sdk";

const executor = new StageExecutor(query, {
  model: "claude-sonnet-4-6",
});
const prompt = buildStagePrompt("issue-pickup", { issueNumber: 42 });

for await (const event of executor.execute(prompt)) {
  if (event.type === "text") console.log(event.text);
}
```

### ContextManager

Manages JSON context files for pipeline stage handoffs.

```typescript
import { ContextManager, IssueContextSchema } from "@nightgauge/sdk";

const ctx = new ContextManager("/path/to/repo");

// Read context with validation
const issueContext = await ctx.read(42, "issue", IssueContextSchema);

// Write context
await ctx.write(42, "planning", planningData);
```

### EventBus

Event system for monitoring pipeline execution.

```typescript
orchestrator.events.on("stage:start", (e) => console.log(`Starting ${e.stage}`));
orchestrator.events.on("stage:complete", (e) => console.log(`Done: ${e.stage}`));
orchestrator.events.on("stage:error", (e) => console.error(e.error));
orchestrator.events.on("token:usage", (e) => console.log(`Tokens: ${e.usage}`));
```

### TokenTracker

Tracks token usage and costs per stage.

```typescript
import { TokenTracker } from "@nightgauge/sdk";

const tracker = new TokenTracker();
tracker.recordStage("issue-pickup", sdkUsage);

const total = tracker.getTotal();
console.log(`Total: ${total.inputTokens} input, ${total.outputTokens} output`);
console.log(`Cost: $${total.costUsd.toFixed(4)}`);
```

## Context Schemas

Pre-defined Zod schemas matching the context file specifications:

```typescript
import {
  IssueContextSchema,
  PlanningContextSchema,
  DevContextSchema,
  PRContextSchema,
  type IssueContext,
  type PlanningContext,
} from "@nightgauge/sdk";
```

## Architecture

This SDK implements the **context-isolated pipeline** architecture described in
[docs/ARCHITECTURE.md](../../docs/ARCHITECTURE.md):

- Each stage runs as an independent Agent SDK query
- Context passed via JSON files, not conversation history
- Constant token usage per stage (~5K) instead of cumulative
- Stages can be retried independently

```
PipelineOrchestrator
    ├── StageExecutor (runs each stage as fresh query)
    ├── ContextManager (reads/writes JSON handoff files)
    ├── EventBus (emits stage lifecycle events)
    └── TokenTracker (records usage per stage)
```

## Requirements

- Node.js 18+
- For `claude-sdk` adapter: separately installed optional peer
  `@anthropic-ai/claude-agent-sdk` and `ANTHROPIC_API_KEY`
- For `claude-headless` adapter: authenticated `claude` CLI
- The VS Code extension always uses `claude-headless` for its Claude selection;
  it never bundles or auto-selects the Agent SDK.
- For `codex` adapter: authenticated `codex` CLI and authenticated `gh` CLI
- For `gemini` adapter: authenticated `gemini` CLI (v0.29.0+)
- For `gemini-sdk` adapter: `@google/genai` and `GEMINI_API_KEY` (or
  `GOOGLE_API_KEY`)

## Related

- [Nightgauge](../../README.md) - Full pipeline documentation
- [CI Integration Guide](../../docs/CI_INTEGRATION.md) - CLI and CI/CD setup
- [Context Architecture](../../docs/CONTEXT_ARCHITECTURE.md) - Context file
  specifications
- [Agent Skills](https://agentskills.io) - Universal skill specification

---

**Author:** nightgauge **License:** Apache-2.0
