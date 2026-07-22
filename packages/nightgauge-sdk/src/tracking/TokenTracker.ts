/**
 * TokenTracker - Tracks token usage per pipeline stage
 *
 * Records input/output tokens, cache usage, and cost per stage.
 * Provides cumulative totals for the entire pipeline run.
 */

import type { PipelineStage } from "../events/EventBus.js";
import { zeroUsage, type WorkflowAgentUsage } from "../cli/workflow/WorkflowEvent.js";
import type { PTCStageUsage } from "./PTCMetrics.js";

/**
 * Token usage statistics for a single stage
 */
export interface StageUsage {
  stage: PipelineStage;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  costUsd: number;
  durationMs: number;
  model: string;
  timestamp: Date;
  /** True when the stage used local inference (e.g., LM Studio) — cost is zero with nonzero tokens. @see Issue #2055 */
  isLocalModel?: boolean;
  /** Premium requests consumed (Copilot-specific). Undefined for non-Copilot adapters. @see Issue #1944 */
  premiumRequests?: number;
}

/**
 * Aggregated usage statistics
 */
export interface TotalUsage {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  costUsd: number;
  durationMs: number;
  stageCount: number;
}

/**
 * SDK result message usage structure (subset of SDKResultMessage)
 */
export interface SDKUsage {
  input_tokens?: number;
  output_tokens?: number;
  cache_read_input_tokens?: number;
  cache_creation_input_tokens?: number;
  /** Copilot-specific: premium requests consumed per stage invocation. @see Issue #1944 */
  premium_requests?: number;
}

/**
 * Model usage mapping from SDK result
 */
export interface SDKModelUsage {
  [model: string]: {
    inputTokens: number;
    outputTokens: number;
  };
}

/**
 * SDK result message interface (subset for usage tracking)
 */
export interface SDKResultMessage {
  type: "result";
  usage?: SDKUsage;
  total_cost_usd?: number;
  modelUsage?: SDKModelUsage;
  /** Top-level model field set by adapters that don't use modelUsage (e.g., LM Studio). @see Issue #2055 */
  model?: string;
}

/**
 * TokenTracker class for monitoring pipeline token consumption
 *
 * @example
 * ```typescript
 * const tracker = new TokenTracker();
 *
 * // Record usage from SDK result
 * tracker.record('issue-pickup', result, 1500);
 *
 * // Get stage-specific usage
 * const stageUsage = tracker.getStageUsage('issue-pickup');
 *
 * // Get cumulative totals
 * const totals = tracker.getTotalUsage();
 * console.log(`Total cost: $${totals.costUsd.toFixed(4)}`);
 * ```
 */
/**
 * Per-node usage recorded for a fanned-out workflow node (`SubAgentNode` /
 * `JudgeVerdict`). The `WorkflowExecutor` (#3908) records one of these per
 * terminal node so a nested fan-out's cost rolls up into the pipeline totals
 * without colliding with the owning stage's own SDK-result record.
 */
export interface WorkflowNodeUsage {
  /** The fanned-out node's id (e.g. `agent:run-1:0:3`). */
  nodeId: string;
  /** Owning pipeline stage the fan-out ran under, when nested. */
  stage?: string;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  costUsd: number;
  /** True when the provider could not report a real cost (non-Claude fan-out). */
  estimated: boolean;
}

export class TokenTracker {
  private stageUsage: Map<PipelineStage, StageUsage> = new Map();
  private ptcUsage: Map<string, PTCStageUsage> = new Map();
  private workflowNodeUsage: Map<string, WorkflowNodeUsage> = new Map();

  /**
   * Record token usage for a completed stage
   */
  record(stage: PipelineStage, result: SDKResultMessage, durationMs: number): void {
    const usage = result.usage ?? {};
    const modelUsage = result.modelUsage ?? {};

    // Prefer top-level model field (LM Studio pattern) over modelUsage key (Anthropic SDK pattern).
    const model = result.model ?? Object.keys(modelUsage)[0] ?? "unknown";

    const hasTokens = (usage.input_tokens ?? 0) > 0 || (usage.output_tokens ?? 0) > 0;
    const isLocalModel = (result.total_cost_usd ?? -1) === 0 && hasTokens;

    const stageData: StageUsage = {
      stage,
      inputTokens: usage.input_tokens ?? 0,
      outputTokens: usage.output_tokens ?? 0,
      cacheReadTokens: usage.cache_read_input_tokens ?? 0,
      cacheCreationTokens: usage.cache_creation_input_tokens ?? 0,
      costUsd: result.total_cost_usd ?? 0,
      durationMs,
      model,
      isLocalModel,
      timestamp: new Date(),
      premiumRequests: usage.premium_requests,
    };

    this.stageUsage.set(stage, stageData);
  }

  /**
   * Get usage for a specific stage
   */
  getStageUsage(stage: PipelineStage): StageUsage | undefined {
    return this.stageUsage.get(stage);
  }

  /**
   * Project a recorded stage's usage onto the canonical {@link WorkflowAgentUsage}
   * shape carried by the workflow event tree (SubAgentNode.usage).
   *
   * This is the SINGLE SOURCE OF TRUTH the emitter folds into the agent terminal
   * node so the acmeapp "zeros + category:unknown" gap (#3914) cannot recur:
   * the terminal node always reflects what the tracker actually recorded rather
   * than whatever progress ticks happened to reach the emitter before a stage
   * threw. Returns a zeroed (non-estimated) record when the stage was never
   * recorded so the field stays REQUIRED and never blank.
   *
   * `estimated` is `true` for local-inference stages (real tokens, zero cost) —
   * the provider cannot report a real USD cost, so the cost is an estimate. It
   * is `false` for providers that report real costs (Claude).
   */
  getWorkflowUsage(stage: PipelineStage): WorkflowAgentUsage {
    const recorded = this.stageUsage.get(stage);
    if (!recorded) {
      return zeroUsage();
    }
    return {
      inputTokens: recorded.inputTokens,
      outputTokens: recorded.outputTokens,
      cacheReadTokens: recorded.cacheReadTokens,
      cacheCreationTokens: recorded.cacheCreationTokens,
      costUsd: recorded.costUsd,
      estimated: recorded.isLocalModel ?? false,
    };
  }

  /**
   * Get cumulative usage across all recorded stages
   */
  getTotalUsage(): TotalUsage {
    let inputTokens = 0;
    let outputTokens = 0;
    let cacheReadTokens = 0;
    let cacheCreationTokens = 0;
    let costUsd = 0;
    let durationMs = 0;

    for (const usage of this.stageUsage.values()) {
      inputTokens += usage.inputTokens;
      outputTokens += usage.outputTokens;
      cacheReadTokens += usage.cacheReadTokens;
      cacheCreationTokens += usage.cacheCreationTokens;
      costUsd += usage.costUsd;
      durationMs += usage.durationMs;
    }

    return {
      inputTokens,
      outputTokens,
      cacheReadTokens,
      cacheCreationTokens,
      costUsd,
      durationMs,
      stageCount: this.stageUsage.size,
    };
  }

  /**
   * Get all stage usage as a record
   */
  toJSON(): Record<PipelineStage, StageUsage> {
    const result: Partial<Record<PipelineStage, StageUsage>> = {};
    for (const [stage, usage] of this.stageUsage) {
      result[stage] = usage;
    }
    return result as Record<PipelineStage, StageUsage>;
  }

  /**
   * Check if a stage has been recorded
   */
  hasStage(stage: PipelineStage): boolean {
    return this.stageUsage.has(stage);
  }

  /**
   * Get list of recorded stages
   */
  getRecordedStages(): PipelineStage[] {
    return Array.from(this.stageUsage.keys());
  }

  /**
   * Record PTC usage for a stage (Issue #1071)
   */
  recordPTC(stage: string, usage: PTCStageUsage): void {
    this.ptcUsage.set(stage, usage);
  }

  /**
   * Get PTC usage for a specific stage
   */
  getPTCUsage(stage: string): PTCStageUsage | undefined {
    return this.ptcUsage.get(stage);
  }

  /**
   * Get all PTC usage records
   */
  getAllPTCUsage(): PTCStageUsage[] {
    return Array.from(this.ptcUsage.values());
  }

  /**
   * Record per-node usage for one terminal fanned-out workflow node (#3908).
   * Keyed by `nodeId` so a re-emitted terminal (last-write-wins) overwrites
   * rather than double-counts.
   */
  recordWorkflowNode(usage: WorkflowNodeUsage): void {
    this.workflowNodeUsage.set(usage.nodeId, usage);
  }

  /** Get the recorded usage for a single workflow node. */
  getWorkflowNodeUsage(nodeId: string): WorkflowNodeUsage | undefined {
    return this.workflowNodeUsage.get(nodeId);
  }

  /** Get every recorded workflow-node usage record. */
  getAllWorkflowNodeUsage(): WorkflowNodeUsage[] {
    return Array.from(this.workflowNodeUsage.values());
  }

  /** Aggregated USD cost across every recorded workflow node. */
  getWorkflowCostUsd(): number {
    let total = 0;
    for (const u of this.workflowNodeUsage.values()) total += u.costUsd;
    return total;
  }

  /**
   * Clear all recorded usage
   */
  clear(): void {
    this.stageUsage.clear();
    this.ptcUsage.clear();
    this.workflowNodeUsage.clear();
  }

  /**
   * Format usage summary as a string
   */
  formatSummary(): string {
    const total = this.getTotalUsage();
    const lines: string[] = [
      "=== Token Usage Summary ===",
      `Stages completed: ${total.stageCount}`,
      `Input tokens: ${total.inputTokens.toLocaleString()}`,
      `Output tokens: ${total.outputTokens.toLocaleString()}`,
      `Cache read: ${total.cacheReadTokens.toLocaleString()}`,
      `Cache created: ${total.cacheCreationTokens.toLocaleString()}`,
      `Total cost: $${total.costUsd.toFixed(4)}`,
      `Total duration: ${(total.durationMs / 1000).toFixed(1)}s`,
      "",
      "--- Per Stage ---",
    ];

    for (const [stage, usage] of this.stageUsage) {
      const requestInfo =
        usage.premiumRequests !== undefined ? `, ${usage.premiumRequests} premium req (est.)` : "";
      lines.push(
        `${stage}: ${usage.inputTokens}in/${usage.outputTokens}out${requestInfo}, $${usage.costUsd.toFixed(4)}, ${(usage.durationMs / 1000).toFixed(1)}s`
      );
    }

    return lines.join("\n");
  }
}
