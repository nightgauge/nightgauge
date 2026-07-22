# SDK Usage Cookbook

Practical recipes for using the Nightgauge SDK programmatically — from single-stage runs to complex multi-repo orchestration.

This document complements the [SDK README](../packages/nightgauge-sdk/README.md) with **working examples** and production patterns.

## Prerequisites

- **Node.js** 18+ installed
- **npm** or **yarn** for dependency management
- For `claude-sdk` adapter: `ANTHROPIC_API_KEY` environment variable
- For `claude-headless` adapter: authenticated `claude` CLI
- For `codex` adapter: authenticated `codex` CLI and `gh` CLI
- For `gemini-sdk` adapter: `GEMINI_API_KEY` or `GOOGLE_API_KEY` environment variable
- Git repository with `.nightgauge/` working directory

## Installation

The SDK is not yet published to npm — build it from a clone of this repo:

```bash
git clone https://github.com/nightgauge/nightgauge.git
cd nightgauge && npm install
npm run -w @nightgauge/sdk build
```

If using the `claude-sdk` adapter, also install:

```bash
npm install @anthropic-ai/claude-agent-sdk
```

For Gemini SDK adapter:

```bash
npm install @google/genai
```

---

## Core Concepts

### PipelineOrchestrator

The main entry point for SDK usage. Orchestrates all six pipeline stages with built-in event system, token tracking, and error recovery.

- **Manages state** across stages using the ContextManager
- **Emits events** for monitoring (stage start/complete, approval, errors)
- **Tracks tokens** consumed per stage and cumulative totals
- **Handles timeouts** with configurable global and per-stage limits
- **Supports backtracking** via feedback signals (Issue #1342)

See [PipelineOrchestrator.ts](../packages/nightgauge-sdk/src/orchestrator/PipelineOrchestrator.ts).

### ContextManager

Handles JSON file persistence with Zod schema validation. Each pipeline stage reads its input context from files and writes output context, enabling resumable, debuggable execution.

- **Validates** all context files against Zod schemas before writing
- **Ensures directory structure** exists automatically
- **Provides atomic writes** to prevent partial file corruption
- **Infers stage names** from filenames for better error messages

See [ContextManager.ts](../packages/nightgauge-sdk/src/context/ContextManager.ts) and [CONTEXT_ARCHITECTURE.md](./CONTEXT_ARCHITECTURE.md).

### EventBus

Typed pub/sub event system for real-time monitoring of pipeline execution. Subscribe to stage lifecycle events, token usage, approvals, and backtrack notifications.

Events include:

- `stage:start`, `stage:complete`, `stage:error`, `stage:skipped`, `stage:timeout`
- `approval:needed`, `approval:auto-approved`
- `token:usage`, `phase:start`, `phase:complete`
- `backtrack:triggered`, `backtrack:blocked`

See [EventBus.ts](../packages/nightgauge-sdk/src/events/EventBus.ts).

### TokenTracker

Tracks input/output tokens, cache usage, and costs per stage. Maintains running totals and provides detailed billing information.

- **Records per-stage usage** (model, duration, tokens, cost)
- **Computes total costs** with configurable model pricing
- **Supports cache metrics** (cache read, cache creation tokens)
- **Handles Copilot premium requests** (adapter-specific)

See [TokenTracker.ts](../packages/nightgauge-sdk/src/tracking/TokenTracker.ts).

### StageExecutor

Low-level executor for individual pipeline stages. Used internally by PipelineOrchestrator but also available for custom orchestration.

- **Builds stage prompts** from SKILL.md files
- **Invokes Claude Agent SDK query** function
- **Streams responses** in real-time
- **Handles message collection** and result extraction

See [StageExecutor.ts](../packages/nightgauge-sdk/src/orchestrator/StageExecutor.ts).

### Adapters

Different AI backends are supported via adapter pattern:

- **`claude-sdk`** — Direct Anthropic SDK integration (recommended for SDK users)
- **`claude-headless`** — CLI-based Claude invocation (for shell environments)
- **`codex`** — GitHub Copilot integration via `codex` CLI
- **`gemini`** — Google Gemini via CLI
- **`gemini-sdk`** — Direct Gemini SDK integration

The SDK expects a `SDKQueryFunction` that follows the Claude Agent SDK's `query()` signature. See [StageExecutor.ts](../packages/nightgauge-sdk/src/orchestrator/StageExecutor.ts) for details.

---

## Recipes

### Recipe 1: Run a Complete Pipeline

**Use case:** Fully automate issue-to-PR pipeline in CI/CD without user approval.

```typescript
import { query } from "@anthropic-ai/claude-agent-sdk";
import { PipelineOrchestrator } from "@nightgauge/sdk";

async function main() {
  // Create orchestrator with auto-approval for CI mode
  const orchestrator = new PipelineOrchestrator(query, {
    autoApprove: true, // Skip approval prompts
    globalTimeoutMs: 3600000, // 1 hour timeout
    stageTimeoutMs: 900000, // 15 minutes per stage
    defaultModel: "sonnet", // claude-sonnet-4-6 for cost efficiency
  });

  // Subscribe to important events
  orchestrator.events.on("stage:complete", (event) => {
    console.log(`✓ Completed ${event.stage} in ${event.durationMs}ms`);
  });

  orchestrator.events.on("stage:error", (event) => {
    console.error(`✗ ${event.stage} failed: ${event.error.message}`);
  });

  orchestrator.events.on("token:usage", (event) => {
    console.log(
      `${event.stage}: ${event.inputTokens} in, ${event.outputTokens} out, $${event.costUsd.toFixed(4)}`
    );
  });

  orchestrator.events.on("approval:auto-approved", (event) => {
    console.log(`→ Auto-approved ${event.stage}`);
  });

  // Run the full pipeline
  const result = await orchestrator.run(42); // issue number

  if (result.success) {
    console.log(`\n✓ Pipeline succeeded!`);
    console.log(`  Stages completed: ${result.stagesCompleted.join(", ")}`);
    console.log(`  Total duration: ${result.totalDurationMs}ms`);
    console.log(`  Total cost: $${result.usage.costUsd.toFixed(4)}`);
    console.log(`  Tokens: ${result.usage.inputTokens} in, ${result.usage.outputTokens} out`);
  } else {
    console.error(`\n✗ Pipeline failed`);
    console.error(`  Failed stages: ${result.stagesFailed.join(", ")}`);
    console.error(`  Completed: ${result.stagesCompleted.join(", ")}`);
    process.exit(1);
  }
}

main().catch(console.error);
```

**Expected output:**

```
✓ Completed issue-pickup in 15234ms
✓ Completed feature-planning in 18567ms
→ Auto-approved feature-planning
✓ Completed feature-dev in 42123ms
✓ Completed feature-validate in 35891ms
✓ Completed pr-create in 22456ms
✓ Completed pr-merge in 8901ms

✓ Pipeline succeeded!
  Stages completed: issue-pickup, feature-planning, feature-dev, feature-validate, pr-create, pr-merge
  Total duration: 143172ms
  Total cost: $0.3847
  Tokens: 45321 in, 28934 out
```

**Error handling:**

```typescript
try {
  const result = await orchestrator.run(42);
  if (!result.success) {
    const failedStage = result.stagesFailed[0];
    console.error(`Retry-worthy failure in ${failedStage}`);
    // Optionally restart from the failed stage
  }
} catch (error) {
  if (error instanceof Error) {
    console.error(`Fatal: ${error.message}`);
    // Handle network failures, timeout, etc.
  }
}
```

---

### Recipe 2: Run a Single Stage

**Use case:** Re-run or debug a specific pipeline stage independently.

```typescript
import { PipelineOrchestrator } from "@nightgauge/sdk";
import { query } from "@anthropic-ai/claude-agent-sdk";

async function rerRunStage() {
  const orchestrator = new PipelineOrchestrator(query, {
    cwd: "/path/to/repo",
  });

  // Run just the feature-dev stage for issue 42
  const result = await orchestrator.runStage("feature-dev", 42);

  if (result.success) {
    console.log(`Stage completed in ${result.durationMs}ms`);
    console.log(`Output messages:`, result.messages.length);
  } else {
    console.error(`Stage failed: ${result.error?.message}`);
  }
}

rerRunStage().catch(console.error);
```

**Real-world use case — Debug a failed plan:**

```typescript
async function debugPlanning() {
  const orchestrator = new PipelineOrchestrator(query, {
    cwd: "/path/to/repo",
    autoApprove: false, // Don't auto-approve, so we can review
  });

  // Re-run planning stage
  const result = await orchestrator.runStage("feature-planning", 42);

  if (result.success) {
    // Inspect the generated plan file
    const fs = require("fs");
    const planPath = ".nightgauge/plans/42-*.md";
    console.log("Generated plan:");
    console.log(fs.readFileSync(planPath, "utf-8"));
  }
}
```

---

### Recipe 3: Batch Process Multiple Issues

**Use case:** Queue and execute multiple issues with token budget enforcement.

```typescript
import { PipelineOrchestrator } from "@nightgauge/sdk";
import { query } from "@anthropic-ai/claude-agent-sdk";

interface BatchJob {
  issueNumber: number;
  maxTokenBudget: number;
}

async function batchProcess(jobs: BatchJob[]) {
  const orchestrator = new PipelineOrchestrator(query, {
    autoApprove: true,
    globalTimeoutMs: 7200000, // 2 hours for entire batch
  });

  const results = new Map<number, any>();
  let totalCost = 0;

  for (const job of jobs) {
    console.log(`\nProcessing issue #${job.issueNumber}...`);

    try {
      const result = await orchestrator.run(job.issueNumber);
      results.set(job.issueNumber, result);
      totalCost += result.usage.costUsd;

      console.log(`  ✓ Cost: $${result.usage.costUsd.toFixed(4)}`);
      console.log(`  Budget remaining: $${(job.maxTokenBudget - result.usage.costUsd).toFixed(4)}`);

      // Stop batch if we exceed budget
      if (totalCost > jobs.reduce((sum, j) => sum + j.maxTokenBudget, 0)) {
        console.warn("⚠ Batch budget exceeded, stopping");
        break;
      }
    } catch (error) {
      console.error(`  ✗ Failed: ${error instanceof Error ? error.message : "Unknown error"}`);
      results.set(job.issueNumber, { error, success: false });
    }
  }

  // Summary
  console.log(`\n${"=".repeat(50)}`);
  console.log("Batch Summary:");
  let succeeded = 0;
  let failed = 0;
  for (const [issueNumber, result] of results) {
    if (result.success) {
      succeeded++;
    } else {
      failed++;
    }
  }
  console.log(`  Succeeded: ${succeeded}/${jobs.length}`);
  console.log(`  Failed: ${failed}/${jobs.length}`);
  console.log(`  Total cost: $${totalCost.toFixed(4)}`);
}

// Usage
batchProcess([
  { issueNumber: 100, maxTokenBudget: 0.5 },
  { issueNumber: 101, maxTokenBudget: 0.6 },
  { issueNumber: 102, maxTokenBudget: 0.75 },
]).catch(console.error);
```

---

### Recipe 4: Subscribe to Pipeline Events

**Use case:** Real-time monitoring, progress reporting, and detailed logging.

```typescript
import { PipelineOrchestrator } from "@nightgauge/sdk";
import { query } from "@anthropic-ai/claude-agent-sdk";

function setupEventMonitoring(orchestrator: PipelineOrchestrator) {
  let pipelineStartTime: number;

  orchestrator.events.on("stage:start", (event) => {
    console.log(`⏱  Starting ${event.stage}...`);
    if (event.stage === "issue-pickup") {
      pipelineStartTime = Date.now();
    }
  });

  orchestrator.events.on("stage:complete", (event) => {
    console.log(`✓ ${event.stage} completed in ${(event.durationMs / 1000).toFixed(1)}s`);
  });

  orchestrator.events.on("stage:error", (event) => {
    console.error(`✗ ${event.stage} failed: ${event.error.message}`);
  });

  orchestrator.events.on("stage:timeout", (event) => {
    console.error(`⏲ ${event.stage} timed out (${event.timeoutMs}ms)`);
  });

  orchestrator.events.on("stage:skipped", (event) => {
    console.log(`⊘ Skipped ${event.stage}`);
  });

  orchestrator.events.on("token:usage", (event) => {
    const input = event.inputTokens;
    const output = event.outputTokens;
    const total = input + output;
    const cost = event.costUsd;
    console.log(`  Tokens: ${input} in + ${output} out = ${total} total, cost $${cost.toFixed(4)}`);
  });

  orchestrator.events.on("phase:start", (event) => {
    console.log(`    → ${event.phaseName}...`);
  });

  orchestrator.events.on("phase:complete", (event) => {
    console.log(`    ✓ ${event.phaseName} (${event.durationMs}ms)`);
  });

  orchestrator.events.on("approval:needed", (event) => {
    console.log(`\n🔒 Approval needed for ${event.stage}: ${event.reason}`);
  });

  orchestrator.events.on("approval:auto-approved", (event) => {
    console.log(`→ Auto-approved ${event.stage}`);
  });

  orchestrator.events.on("backtrack:triggered", (event) => {
    console.log(`↶ Backtrack triggered: ${event.reason}`);
    console.log(`  Target stage: ${event.targetStage}`);
  });

  orchestrator.events.on("backtrack:blocked", (event) => {
    console.log(`✗ Backtrack blocked: ${event.reason}`);
  });

  orchestrator.events.on("pipeline:complete", (event) => {
    const elapsed = (Date.now() - pipelineStartTime!) / 1000;
    console.log(`\nPipeline complete in ${elapsed.toFixed(1)}s`);
    console.log(`  Stages: ${event.stagesCompleted.join(", ")}`);
  });
}

async function main() {
  const orchestrator = new PipelineOrchestrator(query, { autoApprove: true });
  setupEventMonitoring(orchestrator);
  await orchestrator.run(42);
}

main().catch(console.error);
```

---

### Recipe 5: Cost Estimation Before Execution

**Use case:** Estimate pipeline cost for an issue before committing tokens.

```typescript
import { TokenTracker } from "@nightgauge/sdk";

// Create a cost estimator based on historical data
interface CostEstimate {
  stage: string;
  avgInputTokens: number;
  avgOutputTokens: number;
  estimatedCostUsd: number;
}

// Historical averages (replace with your own data)
const HISTORICAL_COSTS: Record<string, CostEstimate> = {
  "issue-pickup": {
    stage: "issue-pickup",
    avgInputTokens: 3000,
    avgOutputTokens: 2500,
    estimatedCostUsd: 0.02,
  },
  "feature-planning": {
    stage: "feature-planning",
    avgInputTokens: 5000,
    avgOutputTokens: 4000,
    estimatedCostUsd: 0.04,
  },
  "feature-dev": {
    stage: "feature-dev",
    avgInputTokens: 8000,
    avgOutputTokens: 10000,
    estimatedCostUsd: 0.12,
  },
  "feature-validate": {
    stage: "feature-validate",
    avgInputTokens: 6000,
    avgOutputTokens: 5000,
    estimatedCostUsd: 0.08,
  },
  "pr-create": {
    stage: "pr-create",
    avgInputTokens: 4000,
    avgOutputTokens: 3000,
    estimatedCostUsd: 0.05,
  },
  "pr-merge": {
    stage: "pr-merge",
    avgInputTokens: 2000,
    avgOutputTokens: 1500,
    estimatedCostUsd: 0.02,
  },
};

function estimatePipelineCost(issueComplexity: "simple" | "medium" | "complex" = "medium"): {
  totalCost: number;
  breakdown: CostEstimate[];
} {
  // Adjust estimates based on complexity
  const multiplier = {
    simple: 0.5,
    medium: 1.0,
    complex: 2.0,
  }[issueComplexity];

  const breakdown = Object.values(HISTORICAL_COSTS).map((cost) => ({
    ...cost,
    estimatedCostUsd: cost.estimatedCostUsd * multiplier,
  }));

  const totalCost = breakdown.reduce((sum, item) => sum + item.estimatedCostUsd, 0);

  return { totalCost, breakdown };
}

// Usage
function printCostEstimate(complexity: "simple" | "medium" | "complex") {
  const { totalCost, breakdown } = estimatePipelineCost(complexity);

  console.log(`\nEstimated pipeline cost (${complexity} issue):`);
  console.log("─".repeat(50));
  for (const stage of breakdown) {
    console.log(`  ${stage.stage.padEnd(20)} $${stage.estimatedCostUsd.toFixed(4)}`);
  }
  console.log("─".repeat(50));
  console.log(`  Total: $${totalCost.toFixed(4)}`);
}

printCostEstimate("simple"); // $0.33
printCostEstimate("medium"); // $0.33
printCostEstimate("complex"); // $0.66
```

**With actual tracker usage:**

```typescript
import { PipelineOrchestrator } from "@nightgauge/sdk";
import { query } from "@anthropic-ai/claude-agent-sdk";

async function estimateThenRun(issueNumber: number) {
  // Show estimate
  const { totalCost } = estimatePipelineCost("medium");
  console.log(`Estimated cost: $${totalCost.toFixed(4)}`);
  console.log("Continue? (y/n)");

  // In production, prompt user or check budget
  const maxBudget = 0.5;
  if (totalCost > maxBudget) {
    console.error(`Cost estimate exceeds budget of $${maxBudget.toFixed(4)}`);
    return;
  }

  // Run with tracking
  const orchestrator = new PipelineOrchestrator(query);
  const result = await orchestrator.run(issueNumber);

  if (result.success) {
    console.log(`\nActual cost: $${result.usage.costUsd.toFixed(4)}`);
    console.log(
      `Estimate error: ${(((result.usage.costUsd - totalCost) / totalCost) * 100).toFixed(1)}%`
    );
  }
}
```

---

### Recipe 6: Handle Pipeline Failures and Resume

**Use case:** Implement resilience with retry logic and checkpoint-based resumption.

```typescript
import { PipelineOrchestrator, ContextManager } from "@nightgauge/sdk";
import { query } from "@anthropic-ai/claude-agent-sdk";

interface ResumableExecution {
  issueNumber: number;
  maxRetries: number;
  retryDelay: number; // milliseconds
}

async function executeWithRetry(options: ResumableExecution) {
  const { issueNumber, maxRetries, retryDelay } = options;
  let attempt = 0;

  while (attempt < maxRetries) {
    try {
      const orchestrator = new PipelineOrchestrator(query, {
        autoApprove: true,
      });

      // Subscribe to errors for logging
      orchestrator.events.on("stage:error", (event) => {
        console.error(`Attempt ${attempt + 1}: ${event.stage} failed`);
        console.error(`  Error: ${event.error.message}`);
      });

      const result = await orchestrator.run(issueNumber);

      if (result.success) {
        console.log(`✓ Succeeded on attempt ${attempt + 1}`);
        return result;
      } else {
        console.warn(`Attempt ${attempt + 1} failed at: ${result.stagesFailed.join(", ")}`);
      }
    } catch (error) {
      console.error(
        `Attempt ${attempt + 1} threw: ${error instanceof Error ? error.message : "Unknown"}`
      );
    }

    attempt++;
    if (attempt < maxRetries) {
      console.log(`Retrying in ${retryDelay}ms...`);
      await new Promise((resolve) => setTimeout(resolve, retryDelay));
    }
  }

  throw new Error(`Failed after ${maxRetries} attempts`);
}

// Resume from last successful stage
async function resumeFromLastStage(issueNumber: number) {
  const ctx = new ContextManager(".nightgauge/pipeline");

  // Find the last completed stage by checking which context files exist
  const stageOrder = ["issue", "planning", "dev", "validate", "pr"];
  let lastStage = null;

  for (const stage of stageOrder) {
    const filename = `${stage}-${issueNumber}.json`;
    if (await ctx.exists(filename)) {
      lastStage = stage;
    }
  }

  console.log(`Last completed stage: ${lastStage || "none"}`);

  // Map to pipeline stage name
  const stageMap: Record<string, any> = {
    issue: "feature-planning", // Issue pickup done, start with planning
    planning: "feature-dev",
    dev: "feature-validate",
    validate: "pr-create",
    pr: "pr-merge",
  };

  const nextStage = lastStage ? stageMap[lastStage] : "issue-pickup";
  console.log(`Resuming from: ${nextStage}`);

  const orchestrator = new PipelineOrchestrator(query, {
    autoApprove: true,
    stages: [nextStage, "pr-create", "pr-merge"], // Run from resume point onward
  });

  return orchestrator.run(issueNumber);
}

// Usage
executeWithRetry({
  issueNumber: 42,
  maxRetries: 3,
  retryDelay: 5000,
}).catch(console.error);
```

---

### Recipe 7: Custom Validation Pipeline

**Use case:** Compose stages with custom pre/post validators.

```typescript
import { PipelineOrchestrator } from "@nightgauge/sdk";
import { query } from "@anthropic-ai/claude-agent-sdk";
import * as fs from "fs/promises";

interface StageValidator {
  name: string;
  validate: (issueNumber: number) => Promise<{ valid: boolean; reason?: string }>;
}

// Pre-execution validators
const preValidators: StageValidator[] = [
  {
    name: "Branch exists",
    validate: async (issueNumber) => {
      try {
        const branch = `feat/${issueNumber}-*`;
        // Check if branch exists via git
        return { valid: true };
      } catch {
        return { valid: false, reason: "Branch does not exist" };
      }
    },
  },
  {
    name: "No uncommitted changes",
    validate: async () => {
      // Run git status check
      return { valid: true };
    },
  },
];

// Post-execution validators
const postValidators: StageValidator[] = [
  {
    name: "Context file exists",
    validate: async (issueNumber) => {
      try {
        await fs.stat(`.nightgauge/pipeline/dev-${issueNumber}.json`);
        return { valid: true };
      } catch {
        return { valid: false, reason: "Context file missing" };
      }
    },
  },
  {
    name: "No merge conflicts",
    validate: async () => {
      // Check for merge conflicts in repo
      return { valid: true };
    },
  },
];

async function runValidatedPipeline(issueNumber: number) {
  // Run pre-execution validators
  console.log("Running pre-execution validators...");
  for (const validator of preValidators) {
    const { valid, reason } = await validator.validate(issueNumber);
    if (!valid) {
      console.error(`✗ ${validator.name}: ${reason}`);
      return;
    }
    console.log(`✓ ${validator.name}`);
  }

  // Run pipeline
  const orchestrator = new PipelineOrchestrator(query, {
    autoApprove: true,
  });

  const result = await orchestrator.run(issueNumber);

  // Run post-execution validators
  if (result.success) {
    console.log("\nRunning post-execution validators...");
    for (const validator of postValidators) {
      const { valid, reason } = await validator.validate(issueNumber);
      if (!valid) {
        console.error(`✗ ${validator.name}: ${reason}`);
        // Could trigger rollback or cleanup here
        continue;
      }
      console.log(`✓ ${validator.name}`);
    }
  }

  return result;
}

// Usage
runValidatedPipeline(42).catch(console.error);
```

---

### Recipe 8: Cross-Repo Orchestration

**Use case:** Coordinate work across multiple repositories with shared context.

```typescript
import { PipelineOrchestrator } from "@nightgauge/sdk";
import { query } from "@anthropic-ai/claude-agent-sdk";

interface CrossRepoJob {
  repoPath: string;
  issueNumber: number;
}

async function orchestrateCrossRepo(jobs: CrossRepoJob[]) {
  const results = new Map<string, any>();

  for (const job of jobs) {
    console.log(`\n${"=".repeat(50)}`);
    console.log(`Repository: ${job.repoPath}`);
    console.log(`Issue: #${job.issueNumber}`);
    console.log("=".repeat(50));

    try {
      const orchestrator = new PipelineOrchestrator(query, {
        cwd: job.repoPath, // Switch working directory
        autoApprove: true,
      });

      // Monitor this repo's execution
      orchestrator.events.on("stage:complete", (event) => {
        console.log(`  ✓ ${event.stage}`);
      });

      const result = await orchestrator.run(job.issueNumber);
      results.set(job.repoPath, result);

      if (result.success) {
        console.log(`✓ Completed in ${(result.totalDurationMs / 1000).toFixed(1)}s`);
      }
    } catch (error) {
      console.error(`✗ Failed: ${error instanceof Error ? error.message : "Unknown"}`);
      results.set(job.repoPath, { error, success: false });
    }
  }

  // Summary
  console.log(`\n${"=".repeat(50)}`);
  console.log("Cross-Repo Summary:");
  let totalCost = 0;
  for (const [repo, result] of results) {
    const status = result.success ? "✓" : "✗";
    const cost = result.success ? result.usage.costUsd : 0;
    totalCost += cost;
    console.log(`  ${status} ${repo}: $${cost.toFixed(4)}`);
  }
  console.log(`Total cost: $${totalCost.toFixed(4)}`);
}

// Usage
orchestrateCrossRepo([
  { repoPath: "/path/to/backend", issueNumber: 100 },
  { repoPath: "/path/to/frontend", issueNumber: 200 },
  { repoPath: "/path/to/mobile", issueNumber: 300 },
]).catch(console.error);
```

---

### Recipe 9: Agent Teams for Parallel Work

**Use case:** Break epic into sub-issues and execute them in parallel waves.

```typescript
import { calculateWaves, detectFileConflicts, splitBudget } from "@nightgauge/sdk";
import type { SubIssue, WaveAssignment } from "@nightgauge/sdk";

interface EpicDecomposition {
  epicNumber: number;
  subIssues: SubIssue[];
}

async function orchestrateEpicParallel(epic: EpicDecomposition) {
  console.log(`Epic #${epic.epicNumber}: Parallel execution plan`);
  console.log("=".repeat(50));

  // Step 1: Detect file conflicts
  const conflicts = await detectFileConflicts(epic.subIssues);
  if (conflicts.length > 0) {
    console.log("\nFile Conflicts:");
    for (const conflict of conflicts) {
      console.log(`  ${conflict.severity}: ${conflict.path}`);
      console.log(`    Issues: ${conflict.issues.join(", ")}`);
    }
  }

  // Step 2: Calculate parallel waves
  const waves = await calculateWaves({
    issues: epic.subIssues,
    deps: {}, // No dependencies in this example
  });

  console.log(`\nExecution Plan: ${waves.length} waves`);
  for (const wave of waves) {
    console.log(`  Wave ${wave.waveIndex}:`);
    for (const issue of wave.issues) {
      console.log(`    - Issue #${issue.number}: ${issue.title}`);
    }
  }

  // Step 3: Split budget across sub-issues
  const totalBudget = 5.0; // $5.00 for entire epic
  const budget = await splitBudget(epic.subIssues, totalBudget, "proportional");

  console.log(`\nBudget Allocation (total $${totalBudget.toFixed(2)}):`);
  for (const allocation of budget.allocations) {
    console.log(
      `  Issue #${allocation.issueNumber}: $${allocation.tokenBudget.toFixed(2)} (${(allocation.percentage * 100).toFixed(1)}%)`
    );
  }

  // Step 4: Execute waves in parallel
  console.log(`\nExecuting waves...`);
  for (const wave of waves) {
    console.log(`\n→ Wave ${wave.waveIndex}:`);

    // Execute all issues in this wave in parallel
    const wavePromises = wave.issues.map(async (issue) => {
      console.log(`  Starting #${issue.number}...`);
      // Run orchestrator for each issue
      // This is simplified; actual implementation would create separate orchestrators
      return { issue: issue.number, success: true };
    });

    const waveResults = await Promise.all(wavePromises);
    for (const waveResult of waveResults) {
      if (waveResult.success) {
        console.log(`  ✓ Issue #${waveResult.issue} completed`);
      }
    }
  }
}

// Usage
orchestrateEpicParallel({
  epicNumber: 1000,
  subIssues: [
    { number: 1001, title: "API endpoint", files: ["src/api/routes.ts", "src/api/handlers.ts"] },
    {
      number: 1002,
      title: "Database schema",
      files: ["schema/migrations.sql", "src/db/models.ts"],
    },
    {
      number: 1003,
      title: "Frontend UI",
      files: ["src/components/Feature.tsx", "src/styles/feature.css"],
    },
  ],
}).catch(console.error);
```

---

### Recipe 10: Build a Custom CLI

**Use case:** Create your own CLI tool on top of the SDK for specialized use cases.

```typescript
import { PipelineOrchestrator, loadConfigFromEnv } from "@nightgauge/sdk";
import { query } from "@anthropic-ai/claude-agent-sdk";
import * as cac from "cac";

const cli = cac("my-pipeline-cli");

cli
  .command("run <issue>", "Run full pipeline for an issue")
  .option("--auto-approve", "Skip approval prompts")
  .option("--model <model>", "Model to use (sonnet, opus, haiku)")
  .option("--timeout <ms>", "Global timeout in milliseconds")
  .action(async (issue, options) => {
    const issueNumber = parseInt(issue);

    const config = loadConfigFromEnv({
      autoApprove: options.autoApprove,
      defaultModel: options.model,
      globalTimeoutMs: options.timeout ? parseInt(options.timeout) : undefined,
    });

    const orchestrator = new PipelineOrchestrator(query, config);

    // Add event monitoring
    orchestrator.events.on("stage:complete", (event) => {
      console.log(`✓ ${event.stage} (${(event.durationMs / 1000).toFixed(1)}s)`);
    });

    orchestrator.events.on("stage:error", (event) => {
      console.error(`✗ ${event.stage}: ${event.error.message}`);
    });

    orchestrator.events.on("token:usage", (event) => {
      console.log(
        `  ${event.inputTokens}→${event.outputTokens} tokens, $${event.costUsd.toFixed(4)}`
      );
    });

    try {
      const result = await orchestrator.run(issueNumber);
      if (result.success) {
        console.log(`\n✓ Success! Total cost: $${result.usage.costUsd.toFixed(4)}`);
        process.exit(0);
      } else {
        console.error(`\n✗ Failed at: ${result.stagesFailed.join(", ")}`);
        process.exit(1);
      }
    } catch (error) {
      console.error(`\n✗ Error: ${error instanceof Error ? error.message : "Unknown"}`);
      process.exit(1);
    }
  });

cli
  .command("estimate <issue> [complexity]", "Estimate cost for an issue")
  .action(async (issue, complexity = "medium") => {
    // Use Recipe 5 logic here
    console.log(`Estimating cost for issue #${issue} (${complexity})...`);
  });

cli.command("status <issue>", "Check pipeline status for an issue").action(async (issue) => {
  // Use ContextManager to check which stages completed
  console.log(`Status for issue #${issue}...`);
});

cli.help();
cli.parse();
```

Usage:

```bash
my-pipeline-cli run 42 --auto-approve --model opus
my-pipeline-cli estimate 42 complex
my-pipeline-cli status 42
```

---

## Advanced Patterns

### Error Classification and Recovery

The orchestrator emits detailed errors that can be classified for recovery:

```typescript
import { PipelineOrchestrator } from "@nightgauge/sdk";

orchestrator.events.on("stage:error", (event) => {
  const message = event.error.message.toLowerCase();

  if (message.includes("timeout")) {
    console.log("Timeout error — may succeed on retry");
  } else if (message.includes("approval")) {
    console.log("Approval required — needs human intervention");
  } else if (message.includes("context")) {
    console.log("Context error — may need to resume from earlier stage");
  } else if (message.includes("network")) {
    console.log("Network error — safe to retry");
  }
});
```

### Complexity-Based Configuration

Adjust timeouts and models based on issue complexity:

```typescript
function getOrchestratorConfig(complexity: "simple" | "medium" | "complex"): PipelineConfig {
  const configs = {
    simple: {
      defaultModel: "haiku" as const,
      stageTimeoutMs: 300000, // 5 min per stage
      globalTimeoutMs: 1800000, // 30 min total
    },
    medium: {
      defaultModel: "sonnet" as const,
      stageTimeoutMs: 900000, // 15 min per stage
      globalTimeoutMs: 3600000, // 1 hour total
    },
    complex: {
      defaultModel: "opus" as const,
      stageTimeoutMs: 1800000, // 30 min per stage
      globalTimeoutMs: 7200000, // 2 hours total
    },
  };

  return configs[complexity];
}

// Usage
const orchestrator = new PipelineOrchestrator(query, getOrchestratorConfig("complex"));
```

### Token Budget Enforcement

Track tokens and stop execution if budget is exceeded:

```typescript
let budgetUsed = 0;
const maxBudget = 1.0; // $1.00

orchestrator.events.on("token:usage", (event) => {
  budgetUsed += event.costUsd;
  const percentUsed = (budgetUsed / maxBudget) * 100;

  if (percentUsed > 75) {
    console.warn(`⚠ Budget 75% consumed ($${budgetUsed.toFixed(4)}/$${maxBudget.toFixed(4)})`);
  }

  if (budgetUsed > maxBudget) {
    console.error(`✗ Budget exceeded! Stopping pipeline.`);
    orchestrator.stop();
  }
});
```

---

## Environment Variables Reference

All configuration can be driven by environment variables:

```bash
# Adapter selection (default: claude-headless)
export NIGHTGAUGE_ADAPTER=claude-sdk

# API keys (required for respective adapters)
export ANTHROPIC_API_KEY=sk-ant-...
export GEMINI_API_KEY=...

# Execution mode
export NIGHTGAUGE_AUTO_APPROVE=true
export NIGHTGAUGE_OUTPUT_FORMAT=json

# Timeouts (milliseconds)
export NIGHTGAUGE_TIMEOUT=3600000         # Global timeout
export NIGHTGAUGE_STAGE_TIMEOUT=900000    # Per-stage timeout

# Model selection
export NIGHTGAUGE_MODEL=sonnet
export NIGHTGAUGE_GEMINI_MODEL=gemini-2.5-flash

# Working directory
export NIGHTGAUGE_CWD=/path/to/repo

# Config file path
export NIGHTGAUGE_CONFIG=.nightgauge/config.yaml
```

See [docs/CONFIGURATION.md](./CONFIGURATION.md) for full configuration reference.

---

## Common Pitfalls

### Pitfall 1: Not Awaiting Promises

```typescript
// ✗ Wrong — no await
orchestrator.run(42);
console.log("Done"); // Prints before pipeline runs

// ✓ Correct
await orchestrator.run(42);
console.log("Done"); // Prints after pipeline completes
```

### Pitfall 2: Ignoring Events

```typescript
// ✗ Wrong — no insight into execution
await orchestrator.run(42);

// ✓ Correct — monitor progress
orchestrator.events.on("stage:start", (e) => console.log(`Starting ${e.stage}`));
orchestrator.events.on("stage:error", (e) => console.error(`Failed: ${e.error.message}`));
await orchestrator.run(42);
```

### Pitfall 3: Wrong Working Directory

```typescript
// ✗ Wrong — uses process.cwd(), may not be correct repo
const orchestrator = new PipelineOrchestrator(query);

// ✓ Correct — explicit working directory
const orchestrator = new PipelineOrchestrator(query, {
  cwd: "/path/to/repo",
});
```

### Pitfall 4: Not Handling Errors

```typescript
// ✗ Wrong — exceptions silently fail
try {
  await orchestrator.run(42);
} catch (error) {
  // Empty catch — don't do this!
}

// ✓ Correct — log and respond
try {
  await orchestrator.run(42);
} catch (error) {
  console.error(`Pipeline failed: ${error instanceof Error ? error.message : "Unknown"}`);
  process.exit(1);
}
```

### Pitfall 5: Assuming Context Files Are Always Present

```typescript
// ✗ Wrong — will throw if stage hasn't run yet
const planning = await ctx.read(PlanningContextSchema, `planning-${issue}.json`);

// ✓ Correct — check first
const filename = `planning-${issue}.json`;
const exists = await ctx.exists(filename);
if (exists) {
  const planning = await ctx.read(PlanningContextSchema, filename);
}
```

---

## Related Documentation

- [SDK README](../packages/nightgauge-sdk/README.md) — API reference
- [ARCHITECTURE.md](./ARCHITECTURE.md) — SDK architecture and design
- [CONTEXT_ARCHITECTURE.md](./CONTEXT_ARCHITECTURE.md) — Context file formats and schemas
- [CONFIGURATION.md](./CONFIGURATION.md) — Configuration reference
- [CI_INTEGRATION.md](./CI_INTEGRATION.md) — CI/CD setup and usage
- [PIPELINE_EXECUTION.md](./PIPELINE_EXECUTION.md) — Execution modes (manual vs automated)

---

## Questions?

For issues, feature requests, or feedback on these recipes:

1. Check the [CONTRIBUTING.md](../CONTRIBUTING.md) guide
2. Open an issue on [GitHub](https://github.com/nightgauge/nightgauge)
3. See [docs/ARCHITECTURE.md](./ARCHITECTURE.md) for deeper technical context

---

**Author:** nightgauge
**Version:** 1.0
**Last Updated:** 2026-07-21
